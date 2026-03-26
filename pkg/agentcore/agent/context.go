package agent

import (
	"fmt"

	"github.com/logeable/agent/pkg/agentcore/provider"
)

// ContextBuilder constructs the message array sent into the model.
//
// What:
// Right now it only knows how to prepend a system prompt and append an optional
// conversation summary.
//
// Why:
// Separating context assembly from the loop makes the turn logic easier to read.
// It also mirrors how larger agent systems evolve: prompt composition usually
// grows into its own subsystem.
type ContextBuilder struct {
	SystemPrompt string
}

// BuildMessages assembles the messages for one model call.
//
// Order matters:
// 1. system prompt
// 2. prior history
// 3. current user input
//
// Why:
// Most chat-based models interpret messages positionally. By isolating this
// ordering rule here, the loop can stay focused on orchestration.
func (b ContextBuilder) BuildMessages(
	history []provider.Message,
	summary string,
	userMessage string,
) []provider.Message {
	messages := make([]provider.Message, 0, len(history)+2)

	// Start from the static system prompt and optionally enrich it with a summary.
	//
	// Why:
	// Summaries are a cheap way to preserve older context without replaying the
	// full transcript every time.
	systemPrompt := b.SystemPrompt
	if summary != "" {
		systemPrompt = fmt.Sprintf("%s\n\nConversation summary:\n%s", systemPrompt, summary)
	}
	if systemPrompt != "" {
		messages = append(messages, provider.Message{
			Role:    "system",
			Content: systemPrompt,
		})
	}

	// Replay prior conversation so the model can continue from existing state.
	messages = append(messages, history...)

	// The current user message is always the last item because this is the thing
	// we want the model to answer or act on right now.
	messages = append(messages, provider.Message{
		Role:    "user",
		Content: userMessage,
	})
	return messages
}
