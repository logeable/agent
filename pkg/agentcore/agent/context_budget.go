package agent

import (
	"github.com/logeable/agent/pkg/agentcore/compaction"
	"github.com/logeable/agent/pkg/agentcore/provider"
)

// ContextBudgetReport is the telemetry snapshot for one active-context check.
type ContextBudgetReport struct {
	MessagesBefore        int
	MessagesAfter         int
	EstimatedTokensBefore int
	EstimatedTokensAfter  int
	BudgetTokens          int
	TargetTokens          int
	TriggeredCompaction   bool
	CompactionStrategy    string
	DroppedMessages       int
}

func (l *Loop) applyContextBudget(messages []provider.Message) ([]provider.Message, ContextBudgetReport) {
	report := ContextBudgetReport{
		MessagesBefore: len(messages),
		MessagesAfter:  len(messages),
	}

	if !l.ContextBudget.Enabled() {
		return messages, report
	}

	estimator := l.ContextBudget.estimator()
	before := estimator.EstimateMessages(messages)
	report.EstimatedTokensBefore = before
	report.EstimatedTokensAfter = before
	report.BudgetTokens = l.ContextBudget.MaxInputTokens
	report.TargetTokens = l.ContextBudget.targetTokens()
	if before <= l.ContextBudget.MaxInputTokens {
		return messages, report
	}

	result := l.ContextBudget.compactor().Compact(compaction.ContextCompactInput{
		Messages:        messages,
		EstimatedTokens: before,
		BudgetTokens:    l.ContextBudget.MaxInputTokens,
		TargetTokens:    report.TargetTokens,
	})
	compacted := result.Messages
	if compacted == nil {
		compacted = messages
	}

	after := estimator.EstimateMessages(compacted)
	report.MessagesAfter = len(compacted)
	report.EstimatedTokensAfter = after
	report.TriggeredCompaction = true
	report.CompactionStrategy = result.Strategy
	report.DroppedMessages = result.DroppedMessages
	return compacted, report
}
