package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAIResponseModelDirectResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Fatalf("path = %s, want /responses", r.URL.Path)
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req["model"] != "demo-model" {
			t.Fatalf("model = %v, want demo-model", req["model"])
		}
		if req["instructions"] != "system rules" {
			t.Fatalf("instructions = %v, want system rules", req["instructions"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"output":[
				{"type":"message","content":[{"type":"output_text","text":"hello from responses"}]}
			]
		}`))
	}))
	defer server.Close()

	model, err := NewOpenAIResponseModel(OpenAIResponseConfig{
		BaseURL: server.URL,
		Model:   "demo-model",
	})
	if err != nil {
		t.Fatalf("NewOpenAIResponseModel() error = %v", err)
	}

	resp, err := model.Chat(context.Background(), []Message{
		{Role: "system", Content: "system rules"},
		{Role: "user", Content: "hi"},
	}, nil, "", nil)
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if resp.Content != "hello from responses" {
		t.Fatalf("content = %q, want %q", resp.Content, "hello from responses")
	}
}

func TestOpenAIResponseModelParsesToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"output":[
				{"type":"message","content":[{"type":"output_text","text":"call a tool"}]},
				{"type":"function_call","call_id":"call-1","name":"echo","arguments":"{\"text\":\"hello\"}"}
			]
		}`))
	}))
	defer server.Close()

	model, err := NewOpenAIResponseModel(OpenAIResponseConfig{
		BaseURL: server.URL,
		Model:   "demo-model",
	})
	if err != nil {
		t.Fatalf("NewOpenAIResponseModel() error = %v", err)
	}

	resp, err := model.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, "", nil)
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool call count = %d, want 1", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Name != "echo" {
		t.Fatalf("tool name = %q, want echo", resp.ToolCalls[0].Name)
	}
	if got := resp.ToolCalls[0].Arguments["text"]; got != "hello" {
		t.Fatalf("tool args text = %v, want hello", got)
	}
}

func TestOpenAIResponseModelSerializesToolHistory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []map[string]any `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(req.Input) != 4 {
			t.Fatalf("input item count = %d, want 4", len(req.Input))
		}
		if req.Input[1]["type"] != "message" {
			t.Fatalf("assistant message type = %v, want message", req.Input[1]["type"])
		}
		if req.Input[2]["type"] != "function_call" {
			t.Fatalf("tool call history type = %v, want function_call", req.Input[2]["type"])
		}
		if req.Input[3]["type"] != "function_call_output" {
			t.Fatalf("tool output history type = %v, want function_call_output", req.Input[3]["type"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output":[]}`))
	}))
	defer server.Close()

	model, err := NewOpenAIResponseModel(OpenAIResponseConfig{
		BaseURL: server.URL,
		Model:   "demo-model",
	})
	if err != nil {
		t.Fatalf("NewOpenAIResponseModel() error = %v", err)
	}

	_, err = model.Chat(context.Background(), []Message{
		{Role: "user", Content: "hi"},
		{
			Role:    "assistant",
			Content: "Let me call a tool",
			ToolCalls: []ToolCall{{
				ID:        "call-1",
				Name:      "echo",
				Arguments: map[string]any{"text": "hello"},
			}},
		},
		{Role: "tool", ToolCallID: "call-1", Content: "echo:hello"},
	}, []ToolDefinition{{Name: "echo", Description: "Echo text", Parameters: map[string]any{"type": "object"}}}, "", nil)
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
}

func TestOpenAIResponseModelStreamsContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hel\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"lo\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}]}}\n\n"))
	}))
	defer server.Close()

	model, err := NewOpenAIResponseModel(OpenAIResponseConfig{
		BaseURL: server.URL,
		Model:   "demo-model",
	})
	if err != nil {
		t.Fatalf("NewOpenAIResponseModel() error = %v", err)
	}

	var chunks []string
	resp, err := model.ChatStream(
		context.Background(),
		[]Message{{Role: "user", Content: "hi"}},
		nil,
		"",
		nil,
		func(chunk StreamChunk) {
			chunks = append(chunks, chunk.Delta)
		},
	)
	if err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}
	if resp.Content != "hello" {
		t.Fatalf("content = %q, want hello", resp.Content)
	}
	if len(chunks) != 2 || chunks[0] != "hel" || chunks[1] != "lo" {
		t.Fatalf("chunks = %#v, want [hel lo]", chunks)
	}
}

func TestOpenAIResponseModelStreamsContentWhenCompletedOutputIsEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hel\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.done\",\"text\":\"hello\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"output\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":2,\"total_tokens\":5}}}\n\n"))
	}))
	defer server.Close()

	model, err := NewOpenAIResponseModel(OpenAIResponseConfig{
		BaseURL: server.URL,
		Model:   "demo-model",
	})
	if err != nil {
		t.Fatalf("NewOpenAIResponseModel() error = %v", err)
	}

	var chunks []string
	resp, err := model.ChatStream(
		context.Background(),
		[]Message{{Role: "user", Content: "hi"}},
		nil,
		"",
		nil,
		func(chunk StreamChunk) {
			chunks = append(chunks, chunk.Delta)
		},
	)
	if err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}
	if resp.Content != "hello" {
		t.Fatalf("content = %q, want hello", resp.Content)
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 5 {
		t.Fatalf("usage = %#v, want total_tokens=5", resp.Usage)
	}
	if len(chunks) != 1 || chunks[0] != "hel" {
		t.Fatalf("chunks = %#v, want [hel]", chunks)
	}
}

func TestOpenAIResponseModelStreamsContentFromOutputItemDone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello from item\"}]}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"output\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":4,\"total_tokens\":7}}}\n\n"))
	}))
	defer server.Close()

	model, err := NewOpenAIResponseModel(OpenAIResponseConfig{
		BaseURL: server.URL,
		Model:   "demo-model",
	})
	if err != nil {
		t.Fatalf("NewOpenAIResponseModel() error = %v", err)
	}

	resp, err := model.ChatStream(
		context.Background(),
		[]Message{{Role: "user", Content: "hi"}},
		nil,
		"",
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}
	if resp.Content != "hello from item" {
		t.Fatalf("content = %q, want hello from item", resp.Content)
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 7 {
		t.Fatalf("usage = %#v, want total_tokens=7", resp.Usage)
	}
}

func TestOpenAIResponseModelStreamsToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call-1\",\"name\":\"echo\",\"arguments\":\"\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":0,\"item_id\":\"fc_1\",\"delta\":\"{\\\"text\\\":\\\"he\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":0,\"delta\":\"llo\\\"}\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.function_call_arguments.done\",\"output_index\":0,\"item_id\":\"fc_1\",\"arguments\":\"{\\\"text\\\":\\\"hello\\\"}\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call-1\",\"name\":\"echo\",\"arguments\":\"{\\\"text\\\":\\\"hello\\\"}\"}}\n\n"))
	}))
	defer server.Close()

	model, err := NewOpenAIResponseModel(OpenAIResponseConfig{
		BaseURL: server.URL,
		Model:   "demo-model",
	})
	if err != nil {
		t.Fatalf("NewOpenAIResponseModel() error = %v", err)
	}

	resp, err := model.ChatStream(
		context.Background(),
		[]Message{{Role: "user", Content: "hi"}},
		nil,
		"",
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool call count = %d, want 1", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].ID != "call-1" {
		t.Fatalf("tool call id = %q, want call-1", resp.ToolCalls[0].ID)
	}
	if resp.ToolCalls[0].Name != "echo" {
		t.Fatalf("tool name = %q, want echo", resp.ToolCalls[0].Name)
	}
	if got := resp.ToolCalls[0].Arguments["text"]; got != "hello" {
		t.Fatalf("tool args text = %v, want hello", got)
	}
}

func TestOpenAIResponseModelStreamsToolCallsWithSparseOutputIndex(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"rs_1\",\"type\":\"reasoning\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"rs_1\",\"type\":\"reasoning\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.added\",\"output_index\":1,\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call-1\",\"name\":\"bash\",\"arguments\":\"\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.function_call_arguments.done\",\"output_index\":1,\"item_id\":\"fc_1\",\"arguments\":\"{\\\"command\\\":\\\"pwd\\\"}\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.done\",\"output_index\":1,\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call-1\",\"name\":\"bash\",\"arguments\":\"{\\\"command\\\":\\\"pwd\\\"}\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"output\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":2,\"total_tokens\":5}}}\n\n"))
	}))
	defer server.Close()

	model, err := NewOpenAIResponseModel(OpenAIResponseConfig{
		BaseURL: server.URL,
		Model:   "demo-model",
	})
	if err != nil {
		t.Fatalf("NewOpenAIResponseModel() error = %v", err)
	}

	resp, err := model.ChatStream(
		context.Background(),
		[]Message{{Role: "user", Content: "hi"}},
		nil,
		"",
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool call count = %d, want 1", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].ID != "call-1" {
		t.Fatalf("tool call id = %q, want call-1", resp.ToolCalls[0].ID)
	}
	if resp.ToolCalls[0].Name != "bash" {
		t.Fatalf("tool name = %q, want bash", resp.ToolCalls[0].Name)
	}
	if got := resp.ToolCalls[0].Arguments["command"]; got != "pwd" {
		t.Fatalf("tool args command = %v, want pwd", got)
	}
}

func TestOpenAIResponseModelStreamsReasoning(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.reasoning_text.delta\",\"delta\":\"think \"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.reasoning_text.delta\",\"delta\":\"more\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"done\"}]}]}}\n\n"))
	}))
	defer server.Close()

	model, err := NewOpenAIResponseModel(OpenAIResponseConfig{
		BaseURL: server.URL,
		Model:   "demo-model",
	})
	if err != nil {
		t.Fatalf("NewOpenAIResponseModel() error = %v", err)
	}

	var gotKinds []StreamChunkKind
	var gotDeltas []string
	resp, err := model.ChatStream(
		context.Background(),
		[]Message{{Role: "user", Content: "hi"}},
		nil,
		"",
		nil,
		func(chunk StreamChunk) {
			gotKinds = append(gotKinds, chunk.Kind)
			gotDeltas = append(gotDeltas, chunk.Delta)
		},
	)
	if err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}
	if resp.Content != "done" {
		t.Fatalf("content = %q, want done", resp.Content)
	}
	if len(gotKinds) != 2 || gotKinds[0] != StreamChunkKindReasoning || gotKinds[1] != StreamChunkKindReasoning {
		t.Fatalf("kinds = %#v, want reasoning chunks", gotKinds)
	}
	if len(gotDeltas) != 2 || gotDeltas[0] != "think " || gotDeltas[1] != "more" {
		t.Fatalf("deltas = %#v, want [think  more]", gotDeltas)
	}
}

func TestOpenAIResponseModelPassesCustomOptions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req["reasoning_effort"] != "medium" {
			t.Fatalf("reasoning_effort = %v, want medium", req["reasoning_effort"])
		}
		if req["temperature"] != float64(0.1) {
			t.Fatalf("temperature = %v, want 0.1", req["temperature"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output":[]}`))
	}))
	defer server.Close()

	model, err := NewOpenAIResponseModel(OpenAIResponseConfig{
		BaseURL: server.URL,
		Model:   "demo-model",
	})
	if err != nil {
		t.Fatalf("NewOpenAIResponseModel() error = %v", err)
	}

	_, err = model.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, "", map[string]any{
		"reasoning_effort": "medium",
		"temperature":      0.1,
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
}

func TestOpenAIResponseModelParsesUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],
			"usage":{"input_tokens":13,"output_tokens":5,"total_tokens":18}
		}`))
	}))
	defer server.Close()

	model, err := NewOpenAIResponseModel(OpenAIResponseConfig{
		BaseURL: server.URL,
		Model:   "demo-model",
	})
	if err != nil {
		t.Fatalf("NewOpenAIResponseModel() error = %v", err)
	}

	resp, err := model.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, "", nil)
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if resp.Usage == nil {
		t.Fatal("Usage = nil, want parsed usage")
	}
	if resp.Usage.InputTokens != 13 || resp.Usage.OutputTokens != 5 || resp.Usage.TotalTokens != 18 {
		t.Fatalf("Usage = %+v, want input=13 output=5 total=18", resp.Usage)
	}
}
