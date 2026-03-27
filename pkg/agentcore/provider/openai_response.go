package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

	resp, endpoint, err := m.postResponses(ctx, payload, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		return nil, fmt.Errorf("provider error at %s (%d): %s", endpoint, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var decoded openAIResponseEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
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

	resp, endpoint, err := m.postResponses(ctx, payload, true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		return nil, fmt.Errorf("provider error at %s (%d): %s", endpoint, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return parseOpenAIResponseStream(resp.Body, onChunk)
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
		return nil, fmt.Errorf("send request: %w", err)
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
	ItemID      string                  `json:"item_id"`
	OutputIndex int                     `json:"output_index"`
	Name        string                  `json:"name"`
	Arguments   string                  `json:"arguments"`
	Error       *openAIResponseError    `json:"error"`
	Response    *openAIResponseEnvelope `json:"response"`
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

func parseOpenAIResponseStream(reader io.Reader, onChunk func(StreamChunk)) (*Response, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var dataLines []string
	var accumulated strings.Builder
	var reasoning strings.Builder
	type toolAssembly struct {
		itemID    string
		name      string
		arguments strings.Builder
	}
	toolCalls := make(map[int]*toolAssembly)
	var completed *Response

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
			if evt.Arguments != "" {
				item.arguments.Reset()
				item.arguments.WriteString(evt.Arguments)
			}
		case "response.completed":
			if evt.Response != nil {
				completed = parseOpenAIResponse(evt.Response)
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
	for idx := 0; idx < len(toolCalls); idx++ {
		item := toolCalls[idx]
		if item == nil {
			continue
		}
		args := map[string]any{}
		if raw := strings.TrimSpace(item.arguments.String()); raw != "" {
			if err := json.Unmarshal([]byte(raw), &args); err != nil {
				return nil, fmt.Errorf("decode streamed tool arguments for %q: %w", item.name, err)
			}
		}
		out.ToolCalls = append(out.ToolCalls, ToolCall{
			ID:        item.itemID,
			Name:      item.name,
			Arguments: args,
		})
	}
	return out, nil
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
