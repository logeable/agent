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

const defaultOllamaTimeout = 60 * time.Second

// OllamaConfig describes how to connect to an Ollama API endpoint.
type OllamaConfig struct {
	BaseURL    string
	APIKey     string
	Model      string
	HTTPClient *http.Client
}

// OllamaModel is a provider for Ollama's native `/api/chat` endpoint.
type OllamaModel struct {
	baseURL    string
	apiKey     string
	model      string
	httpClient *http.Client
}

func NewOllamaModel(cfg OllamaConfig) (*OllamaModel, error) {
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
		client = &http.Client{Timeout: defaultOllamaTimeout}
	}

	return &OllamaModel{
		baseURL:    baseURL,
		apiKey:     strings.TrimSpace(cfg.APIKey),
		model:      model,
		httpClient: client,
	}, nil
}

func (m *OllamaModel) Chat(
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

	reqBody := ollamaChatRequest{
		Model:    modelName,
		Messages: serializeOllamaMessages(messages),
		Stream:   false,
	}
	applyOllamaRequestOptions(&reqBody, options)
	if len(tools) > 0 {
		reqBody.Tools = serializeOllamaToolDefinitions(tools)
	}

	payload, err := marshalOllamaChatRequest(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	resp, err := m.doChatRequest(ctx, payload, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		return nil, classifyHTTPError("provider error", resp.StatusCode, string(body), resp.Header)
	}

	var decoded ollamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return parseOllamaChatResponse(decoded), nil
}

func (m *OllamaModel) ChatStream(
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

	reqBody := ollamaChatRequest{
		Model:    modelName,
		Messages: serializeOllamaMessages(messages),
		Stream:   true,
	}
	applyOllamaRequestOptions(&reqBody, options)
	if len(tools) > 0 {
		reqBody.Tools = serializeOllamaToolDefinitions(tools)
	}

	payload, err := marshalOllamaChatRequest(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	resp, err := m.doChatRequest(ctx, payload, true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		return nil, classifyHTTPError("provider error", resp.StatusCode, string(body), resp.Header)
	}

	return parseOllamaChatStream(resp.Body, onChunk)
}

func (m *OllamaModel) doChatRequest(ctx context.Context, payload []byte, stream bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		m.baseURL+"/chat",
		bytes.NewReader(payload),
	)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if stream {
		req.Header.Set("Accept", "application/x-ndjson")
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

type ollamaChatRequest struct {
	Model       string          `json:"model"`
	Messages    []ollamaMessage `json:"messages"`
	Tools       []ollamaTool    `json:"tools,omitempty"`
	Format      any             `json:"format,omitempty"`
	Options     map[string]any  `json:"options,omitempty"`
	Stream      bool            `json:"stream"`
	Think       any             `json:"think,omitempty"`
	KeepAlive   any             `json:"keep_alive,omitempty"`
	LogProbs    *bool           `json:"logprobs,omitempty"`
	TopLogProbs *int            `json:"top_logprobs,omitempty"`
}

type ollamaMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content,omitempty"`
	ToolName  string           `json:"tool_name,omitempty"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
}

type ollamaTool struct {
	Type     string               `json:"type"`
	Function ollamaToolDefinition `json:"function"`
}

type ollamaToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type ollamaToolCall struct {
	Function ollamaFunctionCall `json:"function"`
}

type ollamaFunctionCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

type ollamaChatResponse struct {
	Message struct {
		Content   string           `json:"content"`
		Thinking  string           `json:"thinking"`
		ToolCalls []ollamaToolCall `json:"tool_calls"`
	} `json:"message"`
	Done            bool `json:"done"`
	PromptEvalCount int  `json:"prompt_eval_count"`
	EvalCount       int  `json:"eval_count"`
}

func serializeOllamaMessages(messages []Message) []ollamaMessage {
	out := make([]ollamaMessage, 0, len(messages))
	callNames := make(map[string]string)

	for _, msg := range messages {
		wire := ollamaMessage{
			Role:    msg.Role,
			Content: msg.Content,
		}
		for _, tc := range msg.ToolCalls {
			wire.ToolCalls = append(wire.ToolCalls, ollamaToolCall{
				Function: ollamaFunctionCall{
					Name:      tc.Name,
					Arguments: tc.Arguments,
				},
			})
			if tc.ID != "" {
				callNames[tc.ID] = tc.Name
			}
		}
		if msg.Role == "tool" && msg.ToolCallID != "" {
			wire.ToolName = callNames[msg.ToolCallID]
		}
		out = append(out, wire)
	}
	return out
}

func serializeOllamaToolDefinitions(defs []ToolDefinition) []ollamaTool {
	out := make([]ollamaTool, 0, len(defs))
	for _, def := range defs {
		out = append(out, ollamaTool{
			Type: "function",
			Function: ollamaToolDefinition{
				Name:        def.Name,
				Description: def.Description,
				Parameters:  def.Parameters,
			},
		})
	}
	return out
}

func applyOllamaRequestOptions(req *ollamaChatRequest, options map[string]any) {
	if req == nil || options == nil {
		return
	}
	if format, ok := options["format"]; ok {
		req.Format = format
	}
	if think, ok := options["think"]; ok {
		req.Think = think
	}
	if keepAlive, ok := options["keep_alive"]; ok {
		req.KeepAlive = keepAlive
	}
	if logprobs, ok := boolOption(options, "logprobs"); ok {
		req.LogProbs = &logprobs
	}
	if topLogprobs, ok := intOption(options, "top_logprobs"); ok {
		req.TopLogProbs = &topLogprobs
	}

	runtimeOptions := map[string]any{}
	if raw, ok := options["options"].(map[string]any); ok {
		for k, v := range raw {
			runtimeOptions[k] = v
		}
	}
	if temperature, ok := floatOption(options, "temperature"); ok {
		runtimeOptions["temperature"] = temperature
	}
	if maxOutput, ok := intOption(options, "max_output_tokens"); ok {
		runtimeOptions["num_predict"] = maxOutput
	} else if maxTokens, ok := intOption(options, "max_tokens"); ok {
		runtimeOptions["num_predict"] = maxTokens
	}
	if len(runtimeOptions) > 0 {
		req.Options = runtimeOptions
	}
}

func marshalOllamaChatRequest(req ollamaChatRequest) ([]byte, error) {
	body := map[string]any{
		"model":    req.Model,
		"messages": req.Messages,
		"stream":   req.Stream,
	}
	if len(req.Tools) > 0 {
		body["tools"] = req.Tools
	}
	if req.Format != nil {
		body["format"] = req.Format
	}
	if len(req.Options) > 0 {
		body["options"] = req.Options
	}
	if req.Think != nil {
		body["think"] = req.Think
	}
	if req.KeepAlive != nil {
		body["keep_alive"] = req.KeepAlive
	}
	if req.LogProbs != nil {
		body["logprobs"] = *req.LogProbs
	}
	if req.TopLogProbs != nil {
		body["top_logprobs"] = *req.TopLogProbs
	}
	return json.Marshal(body)
}

func parseOllamaChatResponse(decoded ollamaChatResponse) *Response {
	content := decoded.Message.Content
	if strings.TrimSpace(content) == "" {
		content = extractAnswerFromThinking(decoded.Message.Thinking)
	}
	out := &Response{
		Content: content,
	}
	if decoded.PromptEvalCount > 0 || decoded.EvalCount > 0 {
		out.Usage = &Usage{
			InputTokens:  decoded.PromptEvalCount,
			OutputTokens: decoded.EvalCount,
			TotalTokens:  decoded.PromptEvalCount + decoded.EvalCount,
		}
	}
	for i, tc := range decoded.Message.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, ToolCall{
			ID:        fmt.Sprintf("ollama-call-%d", i),
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}
	return out
}

func parseOllamaChatStream(reader io.Reader, onChunk func(StreamChunk)) (*Response, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var content strings.Builder
	var reasoning strings.Builder
	var final *ollamaChatResponse
	callByName := make(map[string]ToolCall)
	callOrder := make([]string, 0)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var chunk ollamaChatResponse
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			return nil, fmt.Errorf("decode stream chunk: %w", err)
		}

		if delta := chunk.Message.Content; delta != "" {
			content.WriteString(delta)
			if onChunk != nil {
				onChunk(StreamChunk{
					Kind:        StreamChunkKindOutputText,
					Delta:       delta,
					Accumulated: content.String(),
				})
			}
		}
		if delta := chunk.Message.Thinking; delta != "" {
			reasoning.WriteString(delta)
			if onChunk != nil {
				onChunk(StreamChunk{
					Kind:        StreamChunkKindReasoning,
					Delta:       delta,
					Accumulated: reasoning.String(),
				})
			}
		}
		for _, tc := range chunk.Message.ToolCalls {
			name := tc.Function.Name
			if name == "" {
				continue
			}
			if _, ok := callByName[name]; !ok {
				callOrder = append(callOrder, name)
			}
			callByName[name] = ToolCall{
				ID:        fmt.Sprintf("ollama-call-%d", len(callOrder)-1),
				Name:      name,
				Arguments: tc.Function.Arguments,
			}
		}
		if chunk.Done {
			final = &chunk
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read stream: %w", err)
	}

	resp := &Response{
		Content: content.String(),
	}
	if strings.TrimSpace(resp.Content) == "" {
		resp.Content = extractAnswerFromThinking(reasoning.String())
	}
	if final != nil && (final.PromptEvalCount > 0 || final.EvalCount > 0) {
		resp.Usage = &Usage{
			InputTokens:  final.PromptEvalCount,
			OutputTokens: final.EvalCount,
			TotalTokens:  final.PromptEvalCount + final.EvalCount,
		}
	}
	for _, name := range callOrder {
		resp.ToolCalls = append(resp.ToolCalls, callByName[name])
	}
	return resp, nil
}

func extractAnswerFromThinking(thinking string) string {
	thinking = strings.TrimSpace(thinking)
	if thinking == "" {
		return ""
	}

	const (
		openTag  = "<answer>"
		closeTag = "</answer>"
	)
	start := strings.Index(thinking, openTag)
	if start < 0 {
		return ""
	}
	start += len(openTag)
	end := strings.Index(thinking[start:], closeTag)
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(thinking[start : start+end])
}

func boolOption(options map[string]any, key string) (bool, bool) {
	if options == nil {
		return false, false
	}
	v, ok := options[key]
	if !ok {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}
