package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOllamaModelDirectResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat" {
			t.Fatalf("path = %q, want /chat", r.URL.Path)
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req["model"] != "qwen3" {
			t.Fatalf("model = %v, want qwen3", req["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"content":"hello from ollama"},"done":true,"prompt_eval_count":9,"eval_count":4}`))
	}))
	defer server.Close()

	model, err := NewOllamaModel(OllamaConfig{
		BaseURL: server.URL,
		Model:   "qwen3",
	})
	if err != nil {
		t.Fatalf("NewOllamaModel() error = %v", err)
	}

	resp, err := model.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, "", nil)
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if resp.Content != "hello from ollama" {
		t.Fatalf("content = %q, want hello from ollama", resp.Content)
	}
	if resp.Usage == nil || resp.Usage.InputTokens != 9 || resp.Usage.OutputTokens != 4 || resp.Usage.TotalTokens != 13 {
		t.Fatalf("usage = %+v, want input=9 output=4 total=13", resp.Usage)
	}
}

func TestOllamaModelParsesToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"message":{
				"content":"call a tool",
				"tool_calls":[
					{"function":{"name":"echo","arguments":{"text":"hello"}}}
				]
			},
			"done":true
		}`))
	}))
	defer server.Close()

	model, err := NewOllamaModel(OllamaConfig{
		BaseURL: server.URL,
		Model:   "qwen3",
	})
	if err != nil {
		t.Fatalf("NewOllamaModel() error = %v", err)
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

func TestOllamaModelSerializesToolResultsWithToolName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []map[string]any `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(req.Messages) != 3 {
			t.Fatalf("messages len = %d, want 3", len(req.Messages))
		}
		toolMsg := req.Messages[2]
		if toolMsg["role"] != "tool" {
			t.Fatalf("tool message role = %v, want tool", toolMsg["role"])
		}
		if toolMsg["tool_name"] != "echo" {
			t.Fatalf("tool_name = %v, want echo", toolMsg["tool_name"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"content":"done"},"done":true}`))
	}))
	defer server.Close()

	model, err := NewOllamaModel(OllamaConfig{
		BaseURL: server.URL,
		Model:   "qwen3",
	})
	if err != nil {
		t.Fatalf("NewOllamaModel() error = %v", err)
	}

	_, err = model.Chat(context.Background(), []Message{
		{Role: "user", Content: "hi"},
		{
			Role: "assistant",
			ToolCalls: []ToolCall{{
				ID:        "call-1",
				Name:      "echo",
				Arguments: map[string]any{"text": "hello"},
			}},
		},
		{Role: "tool", ToolCallID: "call-1", Content: "hello"},
	}, nil, "", nil)
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
}

func TestOllamaModelStreamsContentAndReasoning(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte("{\"message\":{\"thinking\":\"think \",\"content\":\"hel\"},\"done\":false}\n"))
		_, _ = w.Write([]byte("{\"message\":{\"thinking\":\"more\",\"content\":\"lo\"},\"done\":false}\n"))
		_, _ = w.Write([]byte("{\"message\":{},\"done\":true,\"prompt_eval_count\":11,\"eval_count\":7}\n"))
	}))
	defer server.Close()

	model, err := NewOllamaModel(OllamaConfig{
		BaseURL: server.URL,
		Model:   "qwen3",
	})
	if err != nil {
		t.Fatalf("NewOllamaModel() error = %v", err)
	}

	var kinds []StreamChunkKind
	var deltas []string
	resp, err := model.ChatStream(
		context.Background(),
		[]Message{{Role: "user", Content: "hi"}},
		nil,
		"",
		nil,
		func(chunk StreamChunk) {
			kinds = append(kinds, chunk.Kind)
			deltas = append(deltas, chunk.Delta)
		},
	)
	if err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}
	if resp.Content != "hello" {
		t.Fatalf("content = %q, want hello", resp.Content)
	}
	if resp.Usage == nil || resp.Usage.InputTokens != 11 || resp.Usage.OutputTokens != 7 || resp.Usage.TotalTokens != 18 {
		t.Fatalf("usage = %+v, want input=11 output=7 total=18", resp.Usage)
	}
	if len(kinds) != 4 {
		t.Fatalf("chunk kinds len = %d, want 4", len(kinds))
	}
	if kinds[0] != StreamChunkKindOutputText || kinds[1] != StreamChunkKindReasoning || kinds[2] != StreamChunkKindOutputText || kinds[3] != StreamChunkKindReasoning {
		t.Fatalf("kinds = %#v", kinds)
	}
	if deltas[0] != "hel" || deltas[1] != "think " || deltas[2] != "lo" || deltas[3] != "more" {
		t.Fatalf("deltas = %#v", deltas)
	}
}
