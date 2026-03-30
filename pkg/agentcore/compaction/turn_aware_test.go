package compaction

import (
	"testing"

	"github.com/logeable/agent/pkg/agentcore/provider"
)

func TestTurnAwareCompactorKeepsWholeTurns(t *testing.T) {
	compactor := TurnAwareCompactor{}
	messages := []provider.Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "older user"},
		{Role: "assistant", Content: "older answer"},
		{Role: "user", Content: "middle user"},
		{Role: "assistant", Content: "middle answer"},
		{Role: "user", Content: "latest user"},
	}

	result := compactor.Compact(ContextCompactInput{
		Messages:     messages,
		TargetTokens: 43,
	})

	if len(result.Messages) != 4 {
		t.Fatalf("compacted messages len = %d, want 4", len(result.Messages))
	}
	if result.Messages[0].Role != "system" {
		t.Fatalf("first message = %+v, want system", result.Messages[0])
	}
	if result.Messages[1].Role != "user" || result.Messages[1].Content != "middle user" {
		t.Fatalf("second message = %+v, want middle user", result.Messages[1])
	}
	if result.Messages[2].Role != "assistant" || result.Messages[2].Content != "middle answer" {
		t.Fatalf("third message = %+v, want middle answer", result.Messages[2])
	}
	if result.Messages[3].Role != "user" || result.Messages[3].Content != "latest user" {
		t.Fatalf("fourth message = %+v, want latest user", result.Messages[3])
	}
}

func TestTurnAwareCompactorKeepsToolTransactionsAtomic(t *testing.T) {
	compactor := TurnAwareCompactor{}
	messages := []provider.Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "older user"},
		{
			Role:    "assistant",
			Content: "calling tool",
			ToolCalls: []provider.ToolCall{{
				ID:        "call-1",
				Name:      "echo",
				Arguments: map[string]any{"text": "hello"},
			}},
		},
		{Role: "tool", ToolCallID: "call-1", Content: "echo:hello"},
		{Role: "assistant", Content: "tool finished"},
		{Role: "user", Content: "latest user"},
	}

	result := compactor.Compact(ContextCompactInput{
		Messages:     messages,
		TargetTokens: 40,
	})

	var sawAssistantCall bool
	var sawToolOutput bool
	for _, msg := range result.Messages {
		if len(msg.ToolCalls) > 0 {
			sawAssistantCall = true
		}
		if msg.Role == "tool" && msg.ToolCallID == "call-1" {
			sawToolOutput = true
		}
	}
	if sawAssistantCall != sawToolOutput {
		t.Fatalf("compacted messages = %+v, want assistant tool call and tool output kept or dropped together", result.Messages)
	}
}

func TestTurnAwareCompactorDropsWholeOlderTurnsWhenNeeded(t *testing.T) {
	compactor := TurnAwareCompactor{}
	messages := []provider.Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "older user"},
		{
			Role:    "assistant",
			Content: "calling tool",
			ToolCalls: []provider.ToolCall{{
				ID:        "call-1",
				Name:      "echo",
				Arguments: map[string]any{"text": "hello"},
			}},
		},
		{Role: "tool", ToolCallID: "call-1", Content: "echo:hello"},
		{Role: "assistant", Content: "tool finished"},
		{Role: "user", Content: "latest user"},
	}

	result := compactor.Compact(ContextCompactInput{
		Messages:     messages,
		TargetTokens: 10,
	})

	if len(result.Messages) != 2 {
		t.Fatalf("compacted messages len = %d, want 2", len(result.Messages))
	}
	if result.Messages[0].Role != "system" || result.Messages[1].Role != "user" || result.Messages[1].Content != "latest user" {
		t.Fatalf("compacted messages = %+v, want only system and latest user", result.Messages)
	}
}
