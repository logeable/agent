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

func TestOpenAIResponseModelStreamsToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":0,\"item_id\":\"call-1\",\"name\":\"echo\",\"delta\":\"{\\\"text\\\":\\\"he\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":0,\"delta\":\"llo\\\"}\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.function_call_arguments.done\",\"output_index\":0,\"item_id\":\"call-1\",\"name\":\"echo\",\"arguments\":\"{\\\"text\\\":\\\"hello\\\"}\"}\n\n"))
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
	if resp.ToolCalls[0].Name != "echo" {
		t.Fatalf("tool name = %q, want echo", resp.ToolCalls[0].Name)
	}
	if got := resp.ToolCalls[0].Arguments["text"]; got != "hello" {
		t.Fatalf("tool args text = %v, want hello", got)
	}
}
