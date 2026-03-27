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

const defaultOpenAICompatTimeout = 60 * time.Second

// OpenAICompatConfig describes how to connect to an OpenAI-compatible endpoint.
//
// What:
// It holds the minimum connection settings needed for a real remote model call.
//
// Why:
// A beginner-friendly codebase should make configuration explicit instead of
// burying it in a large framework. This struct is intentionally small so readers
// can see exactly what a real provider needs.
type OpenAICompatConfig struct {
	BaseURL    string
	APIKey     string
	Model      string
	HTTPClient *http.Client
}

// OpenAICompatModel is a minimal real provider implementation.
//
// What:
// It speaks the widely used OpenAI-compatible `chat/completions` HTTP API.
//
// Why:
// Many local and hosted model servers intentionally copy this API shape.
// Supporting it first gives us one provider that can work with:
// - OpenAI
// - local OpenAI-compatible proxies
// - many self-hosted gateways
// without forcing us to reimplement PicoClaw's full provider stack.
type OpenAICompatModel struct {
	baseURL    string
	apiKey     string
	model      string
	httpClient *http.Client
}

// NewOpenAICompatModel constructs a real network-backed ChatModel.
//
// Why:
// We validate the most important fields here so CLI code can stay simple and
// users get immediate, readable errors when configuration is incomplete.
func NewOpenAICompatModel(cfg OpenAICompatConfig) (*OpenAICompatModel, error) {
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
		client = &http.Client{Timeout: defaultOpenAICompatTimeout}
	}

	return &OpenAICompatModel{
		baseURL:    baseURL,
		apiKey:     strings.TrimSpace(cfg.APIKey),
		model:      model,
		httpClient: client,
	}, nil
}

// Chat sends the conversation transcript to the remote provider.
//
// What:
// This method translates our internal Message / ToolDefinition types into the
// JSON schema expected by OpenAI-compatible servers, then translates the HTTP
// response back into our internal Response type.
//
// Why:
// The agent loop should stay provider-agnostic. All HTTP details live here.
func (m *OpenAICompatModel) Chat(
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

	reqBody := openAICompatRequest{
		Model:    modelName,
		Messages: serializeMessages(messages),
	}
	if len(tools) > 0 {
		reqBody.Tools = serializeToolDefinitions(tools)
		reqBody.ToolChoice = "auto"
	}
	if maxTokens, ok := intOption(options, "max_tokens"); ok {
		reqBody.MaxTokens = &maxTokens
	}
	if temperature, ok := floatOption(options, "temperature"); ok {
		reqBody.Temperature = &temperature
	}

	payload, err := marshalOpenAICompatRequest(reqBody, options)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		m.baseURL+"/chat/completions",
		bytes.NewReader(payload),
	)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if m.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+m.apiKey)
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		return nil, fmt.Errorf("provider error (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var decoded openAICompatResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(decoded.Choices) == 0 {
		return &Response{}, nil
	}

	choice := decoded.Choices[0]
	out := &Response{
		Content: choice.Message.Content,
		Usage:   compatUsage(decoded.Usage),
	}
	for _, tc := range choice.Message.ToolCalls {
		arguments := map[string]any{}
		if strings.TrimSpace(tc.Function.Arguments) != "" {
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &arguments); err != nil {
				return nil, fmt.Errorf("decode tool arguments for %q: %w", tc.Function.Name, err)
			}
		}
		out.ToolCalls = append(out.ToolCalls, ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: arguments,
		})
	}
	return out, nil
}

// ChatStream performs the same logical request as Chat, but asks the server
// for an event stream and forwards partial text as it arrives.
//
// Why:
// This lets the loop emit runtime delta events while preserving the same final
// Response abstraction used by non-streaming calls.
func (m *OpenAICompatModel) ChatStream(
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

	reqBody := openAICompatRequest{
		Model:    modelName,
		Messages: serializeMessages(messages),
		Stream:   true,
	}
	if len(tools) > 0 {
		reqBody.Tools = serializeToolDefinitions(tools)
		reqBody.ToolChoice = "auto"
	}
	if maxTokens, ok := intOption(options, "max_tokens"); ok {
		reqBody.MaxTokens = &maxTokens
	}
	if temperature, ok := floatOption(options, "temperature"); ok {
		reqBody.Temperature = &temperature
	}

	payload, err := marshalOpenAICompatRequest(reqBody, options)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		m.baseURL+"/chat/completions",
		bytes.NewReader(payload),
	)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if m.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+m.apiKey)
	}

	client := &http.Client{Transport: m.httpClient.Transport}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		return nil, fmt.Errorf("provider error (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return parseOpenAICompatStream(resp.Body, onChunk)
}

type openAICompatRequest struct {
	Model       string                `json:"model"`
	Messages    []openAICompatMessage `json:"messages"`
	Tools       []openAICompatTool    `json:"tools,omitempty"`
	ToolChoice  string                `json:"tool_choice,omitempty"`
	MaxTokens   *int                  `json:"max_tokens,omitempty"`
	Temperature *float64              `json:"temperature,omitempty"`
	Stream      bool                  `json:"stream,omitempty"`
}

func marshalOpenAICompatRequest(req openAICompatRequest, options map[string]any) ([]byte, error) {
	body := map[string]any{
		"model":    req.Model,
		"messages": req.Messages,
	}
	if len(req.Tools) > 0 {
		body["tools"] = req.Tools
	}
	if req.ToolChoice != "" {
		body["tool_choice"] = req.ToolChoice
	}
	if req.MaxTokens != nil {
		body["max_tokens"] = *req.MaxTokens
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.Stream {
		body["stream"] = true
	}
	mergeExtraRequestOptions(body, options, map[string]struct{}{
		"model":       {},
		"messages":    {},
		"tools":       {},
		"tool_choice": {},
		"max_tokens":  {},
		"temperature": {},
		"stream":      {},
	})
	return json.Marshal(body)
}

type openAICompatMessage struct {
	Role       string                     `json:"role"`
	Content    string                     `json:"content"`
	ToolCallID string                     `json:"tool_call_id,omitempty"`
	ToolCalls  []openAICompatToolCallWire `json:"tool_calls,omitempty"`
}

type openAICompatTool struct {
	Type     string                   `json:"type"`
	Function openAICompatToolFunction `json:"function"`
}

type openAICompatToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type openAICompatToolCallWire struct {
	ID       string                       `json:"id"`
	Type     string                       `json:"type"`
	Function openAICompatFunctionCallWire `json:"function"`
}

type openAICompatFunctionCallWire struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAICompatResponse struct {
	Choices []struct {
		Message struct {
			Content   string                     `json:"content"`
			ToolCalls []openAICompatToolCallWire `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage *openAICompatUsage `json:"usage"`
}

type openAICompatStreamEnvelope struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			Reasoning        string `json:"reasoning"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *openAICompatUsage `json:"usage"`
}

type openAICompatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func serializeMessages(messages []Message) []openAICompatMessage {
	out := make([]openAICompatMessage, 0, len(messages))
	for _, msg := range messages {
		wire := openAICompatMessage{
			Role:       msg.Role,
			Content:    msg.Content,
			ToolCallID: msg.ToolCallID,
		}
		for _, tc := range msg.ToolCalls {
			wire.ToolCalls = append(wire.ToolCalls, openAICompatToolCallWire{
				ID:   tc.ID,
				Type: "function",
				Function: openAICompatFunctionCallWire{
					Name:      tc.Name,
					Arguments: mustMarshalJSONString(tc.Arguments),
				},
			})
		}
		out = append(out, wire)
	}
	return out
}

func serializeToolDefinitions(defs []ToolDefinition) []openAICompatTool {
	out := make([]openAICompatTool, 0, len(defs))
	for _, def := range defs {
		out = append(out, openAICompatTool{
			Type: "function",
			Function: openAICompatToolFunction{
				Name:        def.Name,
				Description: def.Description,
				Parameters:  def.Parameters,
			},
		})
	}
	return out
}

func mustMarshalJSONString(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func intOption(options map[string]any, key string) (int, bool) {
	if options == nil {
		return 0, false
	}
	switch v := options[key].(type) {
	case int:
		return v, true
	case int32:
		return int(v), true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	default:
		return 0, false
	}
}

func floatOption(options map[string]any, key string) (float64, bool) {
	if options == nil {
		return 0, false
	}
	switch v := options[key].(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	default:
		return 0, false
	}
}

func parseOpenAICompatStream(reader io.Reader, onChunk func(StreamChunk)) (*Response, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var accumulated strings.Builder
	var reasoning strings.Builder
	type toolAssembly struct {
		id        string
		name      string
		arguments strings.Builder
	}
	toolCalls := make(map[int]*toolAssembly)
	var usage *Usage

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}

		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}

		var chunk openAICompatStreamEnvelope
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return nil, fmt.Errorf("decode stream chunk: %w", err)
		}
		if len(chunk.Choices) == 0 {
			if chunk.Usage != nil {
				usage = compatUsage(chunk.Usage)
			}
			continue
		}

		delta := chunk.Choices[0].Delta.Content
		if delta != "" {
			accumulated.WriteString(delta)
			if onChunk != nil {
				onChunk(StreamChunk{
					Kind:        StreamChunkKindOutputText,
					Delta:       delta,
					Accumulated: accumulated.String(),
				})
			}
		}

		reasoningDelta := chunk.Choices[0].Delta.ReasoningContent
		if reasoningDelta == "" {
			reasoningDelta = chunk.Choices[0].Delta.Reasoning
		}
		if reasoningDelta != "" {
			reasoning.WriteString(reasoningDelta)
			if onChunk != nil {
				onChunk(StreamChunk{
					Kind:        StreamChunkKindReasoning,
					Delta:       reasoningDelta,
					Accumulated: reasoning.String(),
				})
			}
		}

		for _, tc := range chunk.Choices[0].Delta.ToolCalls {
			item := toolCalls[tc.Index]
			if item == nil {
				item = &toolAssembly{}
				toolCalls[tc.Index] = item
			}
			if tc.ID != "" {
				item.id = tc.ID
			}
			if tc.Function.Name != "" {
				item.name = tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				item.arguments.WriteString(tc.Function.Arguments)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read stream: %w", err)
	}

	out := &Response{
		Content: accumulated.String(),
		Usage:   usage,
	}
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
			ID:        item.id,
			Name:      item.name,
			Arguments: args,
		})
	}
	return out, nil
}

func compatUsage(value *openAICompatUsage) *Usage {
	if value == nil {
		return nil
	}
	return &Usage{
		InputTokens:  value.PromptTokens,
		OutputTokens: value.CompletionTokens,
		TotalTokens:  value.TotalTokens,
	}
}
