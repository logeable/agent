package demo

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/logeable/agent/pkg/agentcore/provider"
)

// RuleBasedModel is a teaching-oriented fake model used by the demo CLI.
//
// What:
// It implements the same ChatModel interface as a real LLM, but its behavior is
// deterministic and rule-based.
//
// Why:
// Newcomers should be able to run the extracted agent loop immediately without
// first creating API keys or understanding provider-specific setup.
// This model lets us demonstrate the full loop:
// user -> model -> tool call -> tool result -> model -> final answer.
type RuleBasedModel struct {
	nextToolCallID atomic.Uint64
}

// Chat returns either a final answer or a tool request based on the latest message.
//
// Why this shape matters:
// The surrounding agent loop should not care whether the model is "real" or
// "fake". As long as it returns provider.Response, the loop works the same way.
func (m *RuleBasedModel) Chat(
	_ context.Context,
	messages []provider.Message,
	_ []provider.ToolDefinition,
	_ string,
	_ map[string]any,
) (*provider.Response, error) {
	if len(messages) == 0 {
		return &provider.Response{Content: "I did not receive any messages."}, nil
	}

	last := messages[len(messages)-1]
	switch last.Role {
	case "tool":
		return m.respondToTool(last), nil
	case "user":
		return m.respondToUser(last), nil
	default:
		return &provider.Response{
			Content: "I am waiting for a user message.",
		}, nil
	}
}

func (m *RuleBasedModel) respondToTool(last provider.Message) *provider.Response {
	return &provider.Response{
		Content: fmt.Sprintf("I used a tool and got this result:\n%s", last.Content),
	}
}

func (m *RuleBasedModel) respondToUser(last provider.Message) *provider.Response {
	input := strings.TrimSpace(last.Content)
	lower := strings.ToLower(input)

	// Ask for the time tool when the user mentions time.
	if strings.Contains(lower, "time") || strings.Contains(input, "时间") {
		return &provider.Response{
			Content: "I should check the current time with a tool.",
			ToolCalls: []provider.ToolCall{
				{
					ID:   m.newToolCallID(),
					Name: "get_time",
					Arguments: map[string]any{
						"timezone": time.Local.String(),
					},
				},
			},
		}
	}

	// Ask for the echo tool when the user explicitly requests repetition.
	if strings.HasPrefix(lower, "echo ") || strings.HasPrefix(lower, "repeat ") {
		text := input
		if parts := strings.SplitN(input, " ", 2); len(parts) == 2 {
			text = parts[1]
		}
		return &provider.Response{
			Content: "I will use the echo tool so you can see a full tool round-trip.",
			ToolCalls: []provider.ToolCall{
				{
					ID:   m.newToolCallID(),
					Name: "echo",
					Arguments: map[string]any{
						"text": text,
					},
				},
			},
		}
	}

	return &provider.Response{
		Content: "This demo agent is running. Try asking for the time, or type `echo hello`.",
	}
}

func (m *RuleBasedModel) newToolCallID() string {
	id := m.nextToolCallID.Add(1)
	return fmt.Sprintf("tool-call-%d", id)
}
