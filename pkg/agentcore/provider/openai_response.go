package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const defaultOpenAIResponseTimeout = 60 * time.Second

// OpenAIResponseConfig describes how to connect to an OpenAI Responses API
// compatible endpoint.
//
// Why:
// This provider intentionally mirrors the same minimal configuration surface as
// OpenAICompatModel so callers can switch transport style without learning a
// new configuration system.
type OpenAIResponseConfig struct {
	BaseURL    string
	APIKey     string
	Model      string
	HTTPClient *http.Client
}

// OpenAIResponseModel is a minimal provider for the `/responses` API.
//
// What:
// It talks to endpoints that follow the OpenAI Responses API shape instead of
// the older chat/completions shape.
//
// Why:
// PicoClaw's codex provider uses Responses-style inputs because that format is
// better suited to mixed message history and tool call history. This adapter
// brings that same interaction style into this smaller codebase.
type OpenAIResponseModel struct {
	baseURL    string
	apiKey     string
	model      string
	httpClient *http.Client
}

func NewOpenAIResponseModel(cfg OpenAIResponseConfig) (*OpenAIResponseModel, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("base URL is required")
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		return nil, fmt.Errorf("model name is required")
	}

	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: defaultOpenAIResponseTimeout}
	}

	return &OpenAIResponseModel{
		baseURL:    baseURL,
		apiKey:     strings.TrimSpace(cfg.APIKey),
		model:      model,
		httpClient: client,
	}, nil
}

func (m *OpenAIResponseModel) Chat(
	ctx context.Context,
	messages []Message,
	tools []ToolDefinition,
	model string,
	options map[string]any,
) (*Response, error) {
	modelName := strings.TrimSpace(model)
	if modelName == "" {
		modelName = m.model
	}
	if modelName == "" {
		return nil, fmt.Errorf("model name is empty")
	}

	instructions, input := serializeResponsesInput(messages)
	reqBody := openAIResponseRequest{
		Model: modelName,
		Input: input,
		Store: false,
	}
	if instructions != "" {
		reqBody.Instructions = instructions
	}
	if len(tools) > 0 {
		reqBody.Tools = serializeResponsesToolDefinitions(tools)
	}
	if maxOutputTokens, ok := intOption(options, "max_output_tokens"); ok {
		reqBody.MaxOutputTokens = &maxOutputTokens
	} else if maxTokens, ok := intOption(options, "max_tokens"); ok {
		reqBody.MaxOutputTokens = &maxTokens
	}
	if temperature, ok := floatOption(options, "temperature"); ok {
		reqBody.Temperature = &temperature
	}

	payload, err := marshalOpenAIResponseRequest(reqBody, options)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	dump, err := newResponsesDebugDump(false, payload)
	if err != nil {
		return nil, err
	}
	defer dump.close()

	resp, endpoint, err := m.postResponses(ctx, payload, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		dump.writeResponse(body)
		return nil, classifyHTTPError(fmt.Sprintf("provider error at %s", endpoint), resp.StatusCode, string(body), resp.Header)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	dump.writeResponse(body)

	var decoded openAIResponseEnvelope
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return parseOpenAIResponse(&decoded), nil
}

func (m *OpenAIResponseModel) ChatStream(
	ctx context.Context,
	messages []Message,
	tools []ToolDefinition,
	model string,
	options map[string]any,
	onChunk func(StreamChunk),
) (*Response, error) {
	modelName := strings.TrimSpace(model)
	if modelName == "" {
		modelName = m.model
	}
	if modelName == "" {
		return nil, fmt.Errorf("model name is empty")
	}

	instructions, input := serializeResponsesInput(messages)
	reqBody := openAIResponseRequest{
		Model:  modelName,
		Input:  input,
		Store:  false,
		Stream: true,
	}
	if instructions != "" {
		reqBody.Instructions = instructions
	}
	if len(tools) > 0 {
		reqBody.Tools = serializeResponsesToolDefinitions(tools)
	}
	if maxOutputTokens, ok := intOption(options, "max_output_tokens"); ok {
		reqBody.MaxOutputTokens = &maxOutputTokens
	} else if maxTokens, ok := intOption(options, "max_tokens"); ok {
		reqBody.MaxOutputTokens = &maxTokens
	}
	if temperature, ok := floatOption(options, "temperature"); ok {
		reqBody.Temperature = &temperature
	}

	payload, err := marshalOpenAIResponseRequest(reqBody, options)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	dump, err := newResponsesDebugDump(true, payload)
	if err != nil {
		return nil, err
	}
	defer dump.close()

	resp, endpoint, err := m.postResponses(ctx, payload, true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		dump.writeResponse(body)
		return nil, classifyHTTPError(fmt.Sprintf("provider error at %s", endpoint), resp.StatusCode, string(body), resp.Header)
	}

	return parseOpenAIResponseStream(dump.wrapResponseBody(resp.Body), onChunk)
}

// postResponses sends one Responses API request to the endpoint implied by the
// configured base URL.
//
// Why:
// This provider keeps base URL semantics strict and predictable: callers choose
// the exact API root, and the provider appends only `/responses`.
func (m *OpenAIResponseModel) postResponses(ctx context.Context, payload []byte, stream bool) (*http.Response, string, error) {
	endpoint := m.baseURL + "/responses"
	resp, err := m.doResponsesRequest(ctx, endpoint, payload, stream)
	if err != nil {
		return nil, "", err
	}
	return resp, endpoint, nil
}

func (m *OpenAIResponseModel) doResponsesRequest(ctx context.Context, endpoint string, payload []byte, stream bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		endpoint,
		bytes.NewReader(payload),
	)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	if m.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+m.apiKey)
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, classifyTransportError(err)
	}
	return resp, nil
}

type openAIResponseRequest struct {
	Model           string                    `json:"model"`
	Instructions    string                    `json:"instructions,omitempty"`
	Input           []openAIResponseInputItem `json:"input,omitempty"`
	Tools           []openAIResponseTool      `json:"tools,omitempty"`
	MaxOutputTokens *int                      `json:"max_output_tokens,omitempty"`
	Temperature     *float64                  `json:"temperature,omitempty"`
	Store           bool                      `json:"store"`
	Stream          bool                      `json:"stream,omitempty"`
}

func marshalOpenAIResponseRequest(req openAIResponseRequest, options map[string]any) ([]byte, error) {
	body := map[string]any{
		"model": req.Model,
		"store": req.Store,
	}
	if req.Instructions != "" {
		body["instructions"] = req.Instructions
	}
	if len(req.Input) > 0 {
		body["input"] = req.Input
	}
	if len(req.Tools) > 0 {
		body["tools"] = req.Tools
	}
	if req.MaxOutputTokens != nil {
		body["max_output_tokens"] = *req.MaxOutputTokens
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.Stream {
		body["stream"] = true
	}
	mergeExtraRequestOptions(body, options, map[string]struct{}{
		"model":             {},
		"instructions":      {},
		"input":             {},
		"tools":             {},
		"max_output_tokens": {},
		"temperature":       {},
		"store":             {},
		"stream":            {},
	})
	return json.Marshal(body)
}

type openAIResponseInputItem struct {
	Type      string                           `json:"type"`
	Role      string                           `json:"role,omitempty"`
	Content   []openAIResponseInputContentItem `json:"content,omitempty"`
	CallID    string                           `json:"call_id,omitempty"`
	Name      string                           `json:"name,omitempty"`
	Arguments string                           `json:"arguments,omitempty"`
	Output    string                           `json:"output,omitempty"`
}

type openAIResponseInputContentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type openAIResponseTool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	Strict      bool           `json:"strict,omitempty"`
}

type openAIResponseEnvelope struct {
	Output []struct {
		Type      string `json:"type"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
		Content   []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
	Usage *openAIResponseUsage `json:"usage"`
}

type openAIResponseStreamEvent struct {
	Type        string                  `json:"type"`
	Delta       string                  `json:"delta"`
	Text        string                  `json:"text"`
	ItemID      string                  `json:"item_id"`
	OutputIndex int                     `json:"output_index"`
	Name        string                  `json:"name"`
	CallID      string                  `json:"call_id"`
	Arguments   string                  `json:"arguments"`
	Error       *openAIResponseError    `json:"error"`
	Response    *openAIResponseEnvelope `json:"response"`
	Item        *openAIResponseOutput   `json:"item"`
	Part        *openAIResponseContent  `json:"part"`
}

type openAIResponseOutput struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Content   []openAIResponseContent `json:"content"`
}

type openAIResponseContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type openAIResponseError struct {
	Message string `json:"message"`
}

type openAIResponseUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

func serializeResponsesInput(messages []Message) (string, []openAIResponseInputItem) {
	var instructions string
	items := make([]openAIResponseInputItem, 0, len(messages))

	for _, msg := range messages {
		switch msg.Role {
		case "system":
			if instructions == "" {
				instructions = msg.Content
			} else if msg.Content != "" {
				instructions += "\n\n" + msg.Content
			}
		case "user":
			items = append(items, openAIResponseInputItem{
				Type: "message",
				Role: "user",
				Content: []openAIResponseInputContentItem{{
					Type: "input_text",
					Text: msg.Content,
				}},
			})
		case "assistant":
			if msg.Content != "" {
				items = append(items, openAIResponseInputItem{
					Type: "message",
					Role: "assistant",
					Content: []openAIResponseInputContentItem{{
						Type: "output_text",
						Text: msg.Content,
					}},
				})
			}
			for _, tc := range msg.ToolCalls {
				items = append(items, openAIResponseInputItem{
					Type:      "function_call",
					CallID:    tc.ID,
					Name:      tc.Name,
					Arguments: mustMarshalJSONString(tc.Arguments),
				})
			}
		case "tool":
			items = append(items, openAIResponseInputItem{
				Type:   "function_call_output",
				CallID: msg.ToolCallID,
				Output: msg.Content,
			})
		}
	}

	return instructions, items
}

// SerializeResponsesInputForTest exposes the Responses input mapping for tests.
func SerializeResponsesInputForTest(messages []Message) (string, []map[string]any) {
	instructions, items := serializeResponsesInput(messages)
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		data, err := json.Marshal(item)
		if err != nil {
			continue
		}
		var decoded map[string]any
		if err := json.Unmarshal(data, &decoded); err != nil {
			continue
		}
		out = append(out, decoded)
	}
	return instructions, out
}

func serializeResponsesToolDefinitions(defs []ToolDefinition) []openAIResponseTool {
	out := make([]openAIResponseTool, 0, len(defs))
	for _, def := range defs {
		out = append(out, openAIResponseTool{
			Type:        "function",
			Name:        def.Name,
			Description: def.Description,
			Parameters:  def.Parameters,
			Strict:      false,
		})
	}
	return out
}

func parseOpenAIResponse(resp *openAIResponseEnvelope) *Response {
	if resp == nil {
		return &Response{}
	}

	out := &Response{
		Usage: responsesUsage(resp.Usage),
	}
	var content strings.Builder

	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			for _, block := range item.Content {
				if block.Type == "output_text" || block.Type == "text" {
					content.WriteString(block.Text)
				}
			}
		case "function_call":
			args := map[string]any{}
			if strings.TrimSpace(item.Arguments) != "" {
				if err := json.Unmarshal([]byte(item.Arguments), &args); err != nil {
					args = map[string]any{"raw": item.Arguments}
				}
			}
			out.ToolCalls = append(out.ToolCalls, ToolCall{
				ID:        item.CallID,
				Name:      item.Name,
				Arguments: args,
			})
		}
	}

	out.Content = content.String()
	return out
}

func appendOutputText(dst *strings.Builder, content []openAIResponseContent) {
	for _, block := range content {
		if block.Type == "output_text" || block.Type == "text" {
			dst.WriteString(block.Text)
		}
	}
}

func parseOpenAIResponseStream(reader io.Reader, onChunk func(StreamChunk)) (*Response, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var dataLines []string
	var accumulated strings.Builder
	var reasoning strings.Builder
	debugResponsesStream := os.Getenv("AGENT_DEBUG_RESPONSES_STREAM") == "1"
	type toolAssembly struct {
		itemID    string
		callID    string
		name      string
		arguments strings.Builder
	}
	toolCalls := make(map[int]*toolAssembly)
	var completed *Response
	finalizeToolCalls := func(out *Response) error {
		indexes := make([]int, 0, len(toolCalls))
		for idx := range toolCalls {
			indexes = append(indexes, idx)
		}
		sort.Ints(indexes)
		for _, idx := range indexes {
			item := toolCalls[idx]
			if item == nil {
				continue
			}
			args := map[string]any{}
			if raw := strings.TrimSpace(item.arguments.String()); raw != "" {
				if err := json.Unmarshal([]byte(raw), &args); err != nil {
					return fmt.Errorf("decode streamed tool arguments for %q: %w", item.name, err)
				}
			}
			out.ToolCalls = append(out.ToolCalls, ToolCall{
				ID:        item.callID,
				Name:      item.name,
				Arguments: args,
			})
		}
		return nil
	}

	flush := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		payload := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		payload = strings.TrimSpace(payload)
		if payload == "" || payload == "[DONE]" {
			return nil
		}

		var evt openAIResponseStreamEvent
		if err := json.Unmarshal([]byte(payload), &evt); err != nil {
			return fmt.Errorf("decode stream event: %w", err)
		}
		if debugResponsesStream {
			debugLogOpenAIResponseStreamEvent(evt)
		}

		switch evt.Type {
		case "response.output_text.delta":
			if evt.Delta != "" {
				accumulated.WriteString(evt.Delta)
				if onChunk != nil {
					onChunk(StreamChunk{
						Kind:        StreamChunkKindOutputText,
						Delta:       evt.Delta,
						Accumulated: accumulated.String(),
					})
				}
			}
		case "response.output_text.done":
			if evt.Text != "" && len(evt.Text) > accumulated.Len() {
				hadDelta := accumulated.Len() > 0
				accumulated.Reset()
				accumulated.WriteString(evt.Text)
				if onChunk != nil && !hadDelta {
					onChunk(StreamChunk{
						Kind:        StreamChunkKindOutputText,
						Delta:       evt.Text,
						Accumulated: accumulated.String(),
					})
				}
			}
		case "response.content_part.done":
			if evt.Part != nil && (evt.Part.Type == "output_text" || evt.Part.Type == "text") && evt.Part.Text != "" && accumulated.Len() == 0 {
				accumulated.WriteString(evt.Part.Text)
				if onChunk != nil {
					onChunk(StreamChunk{
						Kind:        StreamChunkKindOutputText,
						Delta:       evt.Part.Text,
						Accumulated: accumulated.String(),
					})
				}
			}
		case "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
			if evt.Delta != "" {
				reasoning.WriteString(evt.Delta)
				if onChunk != nil {
					onChunk(StreamChunk{
						Kind:        StreamChunkKindReasoning,
						Delta:       evt.Delta,
						Accumulated: reasoning.String(),
					})
				}
			}
		case "response.function_call_arguments.delta":
			item := toolCalls[evt.OutputIndex]
			if item == nil {
				item = &toolAssembly{}
				toolCalls[evt.OutputIndex] = item
			}
			if evt.ItemID != "" {
				item.itemID = evt.ItemID
			}
			if evt.Name != "" {
				item.name = evt.Name
			}
			if evt.CallID != "" {
				item.callID = evt.CallID
			}
			if evt.Delta != "" {
				item.arguments.WriteString(evt.Delta)
			}
		case "response.function_call_arguments.done":
			item := toolCalls[evt.OutputIndex]
			if item == nil {
				item = &toolAssembly{}
				toolCalls[evt.OutputIndex] = item
			}
			if evt.ItemID != "" {
				item.itemID = evt.ItemID
			}
			if evt.Name != "" {
				item.name = evt.Name
			}
			if evt.CallID != "" {
				item.callID = evt.CallID
			}
			if evt.Arguments != "" {
				item.arguments.Reset()
				item.arguments.WriteString(evt.Arguments)
			}
		case "response.output_item.added", "response.output_item.done":
			if evt.Item == nil {
				break
			}
			switch evt.Item.Type {
			case "function_call":
				item := toolCalls[evt.OutputIndex]
				if item == nil {
					item = &toolAssembly{}
					toolCalls[evt.OutputIndex] = item
				}
				if evt.Item.ID != "" {
					item.itemID = evt.Item.ID
				}
				if evt.Item.CallID != "" {
					item.callID = evt.Item.CallID
				}
				if evt.Item.Name != "" {
					item.name = evt.Item.Name
				}
				if evt.Item.Arguments != "" {
					item.arguments.Reset()
					item.arguments.WriteString(evt.Item.Arguments)
				}
			case "message":
				if accumulated.Len() == 0 {
					appendOutputText(&accumulated, evt.Item.Content)
				}
			}
		case "response.completed":
			if evt.Response != nil {
				completed = parseOpenAIResponse(evt.Response)
				if completed.Content == "" && accumulated.Len() > 0 {
					completed.Content = accumulated.String()
				}
				if len(completed.ToolCalls) == 0 {
					if err := finalizeToolCalls(completed); err != nil {
						return err
					}
				}
			}
		case "error":
			if evt.Error != nil && strings.TrimSpace(evt.Error.Message) != "" {
				return fmt.Errorf("stream error: %s", strings.TrimSpace(evt.Error.Message))
			}
			return fmt.Errorf("stream error")
		}
		return nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			if err := flush(); err != nil {
				return nil, err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read stream: %w", err)
	}
	if err := flush(); err != nil {
		return nil, err
	}

	if completed != nil {
		return completed, nil
	}

	out := &Response{Content: accumulated.String()}
	if err := finalizeToolCalls(out); err != nil {
		return nil, err
	}
	return out, nil
}

func debugLogOpenAIResponseStreamEvent(evt openAIResponseStreamEvent) {
	parts := []string{fmt.Sprintf("type=%s", evt.Type)}
	if evt.OutputIndex != 0 {
		parts = append(parts, fmt.Sprintf("output_index=%d", evt.OutputIndex))
	}
	if evt.ItemID != "" {
		parts = append(parts, fmt.Sprintf("item_id=%q", evt.ItemID))
	}
	if evt.CallID != "" {
		parts = append(parts, fmt.Sprintf("call_id=%q", evt.CallID))
	}
	if evt.Name != "" {
		parts = append(parts, fmt.Sprintf("name=%q", evt.Name))
	}
	if evt.Delta != "" {
		parts = append(parts, fmt.Sprintf("delta_len=%d", len(evt.Delta)))
	}
	if evt.Text != "" {
		parts = append(parts, fmt.Sprintf("text_len=%d", len(evt.Text)))
	}
	if evt.Arguments != "" {
		parts = append(parts, fmt.Sprintf("arguments_len=%d", len(evt.Arguments)))
	}
	if evt.Item != nil {
		parts = append(parts, fmt.Sprintf("item.type=%q", evt.Item.Type))
		if evt.Item.ID != "" {
			parts = append(parts, fmt.Sprintf("item.id=%q", evt.Item.ID))
		}
		if evt.Item.CallID != "" {
			parts = append(parts, fmt.Sprintf("item.call_id=%q", evt.Item.CallID))
		}
		if evt.Item.Name != "" {
			parts = append(parts, fmt.Sprintf("item.name=%q", evt.Item.Name))
		}
		if len(evt.Item.Content) > 0 {
			parts = append(parts, fmt.Sprintf("item.content_blocks=%d", len(evt.Item.Content)))
		}
		if evt.Item.Arguments != "" {
			parts = append(parts, fmt.Sprintf("item.arguments_len=%d", len(evt.Item.Arguments)))
		}
	}
	if evt.Part != nil {
		parts = append(parts, fmt.Sprintf("part.type=%q", evt.Part.Type))
		if evt.Part.Text != "" {
			parts = append(parts, fmt.Sprintf("part.text_len=%d", len(evt.Part.Text)))
		}
	}
	if evt.Response != nil {
		parts = append(parts, fmt.Sprintf("response.output_len=%d", len(evt.Response.Output)))
		if evt.Response.Usage != nil {
			parts = append(parts, fmt.Sprintf("response.total_tokens=%d", evt.Response.Usage.TotalTokens))
		}
	}
	fmt.Fprintf(os.Stderr, "[openai_response_stream] %s\n", strings.Join(parts, " "))
}

type responsesDebugDump struct {
	requestPath  string
	responsePath string
	responseFile *os.File
}

func newResponsesDebugDump(stream bool, request []byte) (*responsesDebugDump, error) {
	if os.Getenv("AGENT_DEBUG_RESPONSES_DUMP") != "1" {
		return &responsesDebugDump{}, nil
	}

	dir := strings.TrimSpace(os.Getenv("AGENT_DEBUG_RESPONSES_DUMP_DIR"))
	if dir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create responses debug dump dir: %w", err)
	}

	stem := fmt.Sprintf("agent-openai-response-%d", time.Now().UnixNano())
	mode := "json"
	if stream {
		mode = "sse"
	}

	requestPath := filepath.Join(dir, stem+".request.json")
	if err := os.WriteFile(requestPath, request, 0o644); err != nil {
		return nil, fmt.Errorf("write responses request dump: %w", err)
	}

	responsePath := filepath.Join(dir, stem+".response."+mode)
	responseFile, err := os.Create(responsePath)
	if err != nil {
		return nil, fmt.Errorf("create responses response dump: %w", err)
	}

	fmt.Fprintf(os.Stderr, "[openai_response_dump] request=%s response=%s\n", requestPath, responsePath)
	return &responsesDebugDump{
		requestPath:  requestPath,
		responsePath: responsePath,
		responseFile: responseFile,
	}, nil
}

func (d *responsesDebugDump) wrapResponseBody(body io.Reader) io.Reader {
	if d == nil || d.responseFile == nil {
		return body
	}
	return io.TeeReader(body, d.responseFile)
}

func (d *responsesDebugDump) writeResponse(body []byte) {
	if d == nil || d.responseFile == nil {
		return
	}
	_, _ = d.responseFile.Write(body)
}

func (d *responsesDebugDump) close() {
	if d == nil || d.responseFile == nil {
		return
	}
	_ = d.responseFile.Close()
}

func responsesUsage(value *openAIResponseUsage) *Usage {
	if value == nil {
		return nil
	}
	return &Usage{
		InputTokens:  value.InputTokens,
		OutputTokens: value.OutputTokens,
		TotalTokens:  value.TotalTokens,
	}
}
