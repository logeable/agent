package compaction

import "github.com/logeable/agent/pkg/agentcore/provider"

// MessageEstimator estimates the token cost of an active context.
type MessageEstimator interface {
	EstimateMessages(messages []provider.Message) int
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
