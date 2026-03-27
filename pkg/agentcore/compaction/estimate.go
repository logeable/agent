package compaction

import (
	"fmt"
	"math"

	"github.com/logeable/agent/pkg/agentcore/provider"
)

// ApproximateTokenEstimator is a deliberately simple default estimator.
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
	return int(math.Ceil(float64(len(value)) / 4.0))
}
