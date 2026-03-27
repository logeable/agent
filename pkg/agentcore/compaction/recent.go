package compaction

import "github.com/logeable/agent/pkg/agentcore/provider"

// RecentMessageCompactor keeps the system prompt and the newest messages.
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
