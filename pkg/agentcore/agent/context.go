package agent

import (
	"fmt"
	"math"

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

// ContextBudget controls how much active context is sent to the model.
//
// Why:
// Session storage and model input are separate concerns. The budget applies to
// the transient active context for one model call, not to the stored session.
type ContextBudget struct {
	MaxInputTokens int
	TargetFraction float64
	Estimator      MessageEstimator
	Compactor      ContextCompactor
}

// Enabled reports whether the runtime should enforce an active-context budget.
func (b ContextBudget) Enabled() bool {
	return b.MaxInputTokens > 0
}

func (b ContextBudget) targetTokens() int {
	if b.MaxInputTokens <= 0 {
		return 0
	}
	fraction := b.TargetFraction
	if fraction <= 0 || fraction > 1 {
		fraction = 1.0 / 3.0
	}
	target := int(math.Round(float64(b.MaxInputTokens) * fraction))
	if target <= 0 || target > b.MaxInputTokens {
		return b.MaxInputTokens
	}
	return target
}

func (b ContextBudget) estimator() MessageEstimator {
	if b.Estimator != nil {
		return b.Estimator
	}
	return ApproximateTokenEstimator{}
}

func (b ContextBudget) compactor() ContextCompactor {
	if b.Compactor != nil {
		return b.Compactor
	}
	return RecentMessageCompactor{}
}

// MessageEstimator estimates the token cost of an active context.
type MessageEstimator interface {
	EstimateMessages(messages []provider.Message) int
}

// ApproximateTokenEstimator is a deliberately simple default estimator.
//
// Why:
// The core needs a stable, provider-agnostic signal for budget checks before
// adding model-specific tokenizers or billing integrations.
type ApproximateTokenEstimator struct{}

func (ApproximateTokenEstimator) EstimateMessages(messages []provider.Message) int {
	total := 0
	for _, msg := range messages {
		total += estimateMessageTokens(msg)
	}
	return total
}

func estimateMessageTokens(msg provider.Message) int {
	total := 6
	total += roughTokenCount(msg.Role)
	total += roughTokenCount(msg.Content)
	total += roughTokenCount(msg.ToolCallID)
	for _, call := range msg.ToolCalls {
		total += 8
		total += roughTokenCount(call.ID)
		total += roughTokenCount(call.Name)
		for key, value := range call.Arguments {
			total += roughTokenCount(key)
			total += roughTokenCount(fmt.Sprintf("%v", value))
		}
	}
	return total
}

func roughTokenCount(value string) int {
	if value == "" {
		return 0
	}
	// This intentionally overestimates a little. The runtime only needs a
	// stable budget signal, not exact provider billing math.
	return int(math.Ceil(float64(len(value)) / 4.0))
}

// ContextCompactor reduces an oversized active context without mutating the
// stored session transcript.
type ContextCompactor interface {
	Compact(input ContextCompactInput) ContextCompactResult
}

// ContextCompactInput is the data passed into a compaction strategy.
type ContextCompactInput struct {
	Messages        []provider.Message
	EstimatedTokens int
	BudgetTokens    int
	TargetTokens    int
}

// ContextCompactResult is the output of a compaction strategy.
type ContextCompactResult struct {
	Messages        []provider.Message
	Strategy        string
	DroppedMessages int
}

type compactMessageBlock struct {
	messages []provider.Message
}

// RecentMessageCompactor keeps the system prompt and the newest messages.
//
// Why:
// This is a safe first-pass strategy for active context compaction because it
// does not rewrite session state and is easy to reason about.
type RecentMessageCompactor struct{}

func (RecentMessageCompactor) Compact(input ContextCompactInput) ContextCompactResult {
	if len(input.Messages) == 0 {
		return ContextCompactResult{Messages: nil, Strategy: "recent_trim"}
	}

	result := make([]provider.Message, 0, len(input.Messages))
	start := 0
	if input.Messages[0].Role == "system" {
		result = append(result, input.Messages[0])
		start = 1
	}

	estimator := ApproximateTokenEstimator{}
	currentTokens := estimator.EstimateMessages(result)
	blocks := buildCompactBlocks(input.Messages[start:])
	if len(blocks) == 0 {
		return ContextCompactResult{
			Messages:        result,
			Strategy:        "recent_trim",
			DroppedMessages: len(input.Messages) - len(result),
		}
	}

	tail := append([]provider.Message(nil), blocks[len(blocks)-1].messages...)
	currentTokens += estimator.EstimateMessages(tail)

	selected := make([]compactMessageBlock, 0, len(blocks))
	for i := len(blocks) - 2; i >= 0; i-- {
		candidate := blocks[i].messages
		candidateTokens := estimator.EstimateMessages(candidate)
		if len(result) > 0 && currentTokens+candidateTokens > input.TargetTokens {
			continue
		}
		selected = append([]compactMessageBlock{{messages: candidate}}, selected...)
		currentTokens += candidateTokens
	}

	for _, block := range selected {
		result = append(result, block.messages...)
	}
	result = append(result, tail...)

	if len(result) == 0 {
		result = append(result, input.Messages[len(input.Messages)-1])
	}

	return ContextCompactResult{
		Messages:        result,
		Strategy:        "recent_trim",
		DroppedMessages: len(input.Messages) - len(result),
	}
}

func buildCompactBlocks(messages []provider.Message) []compactMessageBlock {
	if len(messages) == 0 {
		return nil
	}

	blocks := make([]compactMessageBlock, 0, len(messages))
	for i := 0; i < len(messages); {
		msg := messages[i]
		if len(msg.ToolCalls) == 0 {
			blocks = append(blocks, compactMessageBlock{
				messages: []provider.Message{msg},
			})
			i++
			continue
		}

		callIDs := make(map[string]struct{}, len(msg.ToolCalls))
		for _, call := range msg.ToolCalls {
			if call.ID != "" {
				callIDs[call.ID] = struct{}{}
			}
		}

		block := []provider.Message{msg}
		j := i + 1
		for ; j < len(messages); j++ {
			next := messages[j]
			if next.Role != "tool" {
				break
			}
			if _, ok := callIDs[next.ToolCallID]; !ok {
				break
			}
			block = append(block, next)
		}
		blocks = append(blocks, compactMessageBlock{messages: block})
		i = j
	}

	return blocks
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
