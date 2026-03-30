package compaction

import "github.com/logeable/agent/pkg/agentcore/provider"

type compactTurn struct {
	messages []provider.Message
}

// TurnAwareCompactor keeps whole turns together instead of trimming at the
// single-message level.
type TurnAwareCompactor struct{}

func (TurnAwareCompactor) Compact(input ContextCompactInput) ContextCompactResult {
	if len(input.Messages) == 0 {
		return ContextCompactResult{Messages: nil, Strategy: "turn_aware_trim"}
	}

	result := make([]provider.Message, 0, len(input.Messages))
	start := 0
	if input.Messages[0].Role == "system" {
		result = append(result, input.Messages[0])
		start = 1
	}

	estimator := ApproximateTokenEstimator{}
	currentTokens := estimator.EstimateMessages(result)
	turns := buildCompactTurns(input.Messages[start:])
	if len(turns) == 0 {
		return ContextCompactResult{
			Messages:        result,
			Strategy:        "turn_aware_trim",
			DroppedMessages: len(input.Messages) - len(result),
		}
	}

	tail := append([]provider.Message(nil), turns[len(turns)-1].messages...)
	currentTokens += estimator.EstimateMessages(tail)

	selected := make([]compactTurn, 0, len(turns))
	for i := len(turns) - 2; i >= 0; i-- {
		candidate := turns[i].messages
		candidateTokens := estimator.EstimateMessages(candidate)
		if len(result) > 0 && currentTokens+candidateTokens > input.TargetTokens {
			continue
		}
		selected = append([]compactTurn{{messages: candidate}}, selected...)
		currentTokens += candidateTokens
	}

	for _, turn := range selected {
		result = append(result, turn.messages...)
	}
	result = append(result, tail...)

	if len(result) == 0 {
		result = append(result, input.Messages[len(input.Messages)-1])
	}

	return ContextCompactResult{
		Messages:        result,
		Strategy:        "turn_aware_trim",
		DroppedMessages: len(input.Messages) - len(result),
	}
}

func buildCompactTurns(messages []provider.Message) []compactTurn {
	blocks := buildCompactBlocks(messages)
	if len(blocks) == 0 {
		return nil
	}

	turns := make([]compactTurn, 0, len(blocks))
	current := make([]provider.Message, 0)
	for _, block := range blocks {
		if len(current) > 0 && len(block.messages) > 0 && block.messages[0].Role == "user" {
			turns = append(turns, compactTurn{messages: current})
			current = make([]provider.Message, 0)
		}
		current = append(current, block.messages...)
	}
	if len(current) > 0 {
		turns = append(turns, compactTurn{messages: current})
	}
	return turns
}
