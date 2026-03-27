package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAICompatModelDirectResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req["model"] != "demo-model" {
			t.Fatalf("model = %v, want demo-model", req["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hello from server"}}]}`))
	}))
	defer server.Close()

	model, err := NewOpenAICompatModel(OpenAICompatConfig{
		BaseURL: server.URL,
		Model:   "demo-model",
	})
	if err != nil {
		t.Fatalf("NewOpenAICompatModel() error = %v", err)
	}

	resp, err := model.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, "", nil)
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if resp.Content != "hello from server" {
		t.Fatalf("content = %q, want %q", resp.Content, "hello from server")
	}
}

func TestOpenAICompatModelParsesToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{
				"message":{
					"content":"call a tool",
					"tool_calls":[{
						"id":"call-1",
						"type":"function",
						"function":{
							"name":"echo",
							"arguments":"{\"text\":\"hello\"}"
						}
					}]
				}
			}]
		}`))
	}))
	defer server.Close()

	model, err := NewOpenAICompatModel(OpenAICompatConfig{
		BaseURL: server.URL,
		Model:   "demo-model",
	})
	if err != nil {
		t.Fatalf("NewOpenAICompatModel() error = %v", err)
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

func TestOpenAICompatModelStreamsContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hel\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	model, err := NewOpenAICompatModel(OpenAICompatConfig{
		BaseURL: server.URL,
		Model:   "demo-model",
	})
	if err != nil {
		t.Fatalf("NewOpenAICompatModel() error = %v", err)
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

func TestOpenAICompatModelStreamsReasoning(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"think \"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"reasoning\":\"more\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	model, err := NewOpenAICompatModel(OpenAICompatConfig{
		BaseURL: server.URL,
		Model:   "demo-model",
	})
	if err != nil {
		t.Fatalf("NewOpenAICompatModel() error = %v", err)
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
	if resp.Content != "done" {
		t.Fatalf("content = %q, want done", resp.Content)
	}
	if len(kinds) != 3 {
		t.Fatalf("chunk kinds len = %d, want 3", len(kinds))
	}
	if kinds[0] != StreamChunkKindReasoning || kinds[1] != StreamChunkKindReasoning || kinds[2] != StreamChunkKindOutputText {
		t.Fatalf("kinds = %#v, want [reasoning reasoning output_text]", kinds)
	}
	if deltas[0] != "think " || deltas[1] != "more" || deltas[2] != "done" {
		t.Fatalf("deltas = %#v, want [think  more done]", deltas)
	}
}

func TestOpenAICompatModelPassesCustomOptions(t *testing.T) {
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
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	model, err := NewOpenAICompatModel(OpenAICompatConfig{
		BaseURL: server.URL,
		Model:   "demo-model",
	})
	if err != nil {
		t.Fatalf("NewOpenAICompatModel() error = %v", err)
	}

	_, err = model.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, "", map[string]any{
		"reasoning_effort": "medium",
		"temperature":      0.1,
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
}
