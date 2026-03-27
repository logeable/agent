package agentclirun

import (
	"fmt"
	"sort"
	"strings"

	"github.com/logeable/agent/pkg/agentcore/agent"
)

// FormatEventLine renders a concise one-line runtime event description.
func FormatEventLine(evt agent.Event) string {
	prefix := fmt.Sprintf("[%s]", evt.Kind)
	switch evt.Kind {
	case agent.EventTurnStarted:
		payload, ok := evt.Payload.(agent.TurnStartedPayload)
		if !ok {
			return prefix
		}
		return fmt.Sprintf("%s session=%s message=%q", prefix, evt.Meta.SessionKey, truncateForLog(payload.UserMessage, 80))
	case agent.EventContextBudget:
		payload, ok := evt.Payload.(agent.ContextBudgetPayload)
		if !ok {
			return prefix
		}
		return fmt.Sprintf("%s iteration=%d estimated=%d budget=%d target=%d compact=%t messages=%d",
			prefix, evt.Meta.Iteration, payload.EstimatedTokensBefore, payload.BudgetTokens, payload.TargetTokens, payload.TriggeredCompaction, payload.MessagesBefore)
	case agent.EventContextCompacted:
		payload, ok := evt.Payload.(agent.ContextCompactedPayload)
		if !ok {
			return prefix
		}
		return fmt.Sprintf("%s iteration=%d strategy=%s estimated_before=%d estimated_after=%d budget=%d target=%d messages_before=%d messages_after=%d dropped=%d",
			prefix, evt.Meta.Iteration, payload.Strategy, payload.EstimatedTokensBefore, payload.EstimatedTokensAfter, payload.BudgetTokens, payload.TargetTokens, payload.MessagesBefore, payload.MessagesAfter, payload.DroppedMessages)
	case agent.EventModelRequest:
		payload, ok := evt.Payload.(agent.ModelRequestPayload)
		if !ok {
			return prefix
		}
		return fmt.Sprintf("%s iteration=%d model=%s messages=%d tools=%d streaming=%t",
			prefix, evt.Meta.Iteration, payload.Model, payload.MessagesCount, payload.ToolsCount, payload.Streaming)
	case agent.EventModelRetry:
		payload, ok := evt.Payload.(agent.ModelRetryPayload)
		if !ok {
			return prefix
		}
		return fmt.Sprintf("%s iteration=%d attempt=%d/%d error_kind=%s reason=%q",
			prefix, evt.Meta.Iteration, payload.Attempt, payload.MaxAttempts, payload.ErrorKind, truncateForLog(payload.Reason, 120))
	case agent.EventModelResponse:
		payload, ok := evt.Payload.(agent.ModelResponsePayload)
		if !ok {
			return prefix
		}
		return fmt.Sprintf("%s iteration=%d content_len=%d tool_calls=%d",
			prefix, evt.Meta.Iteration, payload.ContentLen, payload.ToolCalls)
	case agent.EventModelUsage:
		payload, ok := evt.Payload.(agent.ModelUsagePayload)
		if !ok {
			return prefix
		}
		return fmt.Sprintf("%s iteration=%d input_tokens=%d output_tokens=%d total_tokens=%d",
			prefix, evt.Meta.Iteration, payload.InputTokens, payload.OutputTokens, payload.TotalTokens)
	case agent.EventToolStarted:
		payload, ok := evt.Payload.(agent.ToolStartedPayload)
		if !ok {
			return prefix
		}
		return fmt.Sprintf("%s iteration=%d tool=%s args=%s",
			prefix, evt.Meta.Iteration, payload.Tool, formatArgs(payload.Arguments))
	case agent.EventToolFinished:
		payload, ok := evt.Payload.(agent.ToolFinishedPayload)
		if !ok {
			return prefix
		}
		return fmt.Sprintf("%s iteration=%d tool=%s %s",
			prefix, evt.Meta.Iteration, payload.Tool, formatToolFinishedSummary(payload))
	case agent.EventApprovalRequested:
		payload, ok := evt.Payload.(agent.ApprovalRequestedPayload)
		if !ok {
			return prefix
		}
		line := fmt.Sprintf("%s iteration=%d tool=%s", prefix, evt.Meta.Iteration, payload.Tool)
		if payload.ActionLabel != "" {
			line += fmt.Sprintf(" action=%q", truncateForLog(payload.ActionLabel, 80))
		}
		if payload.Reason != "" {
			line += fmt.Sprintf(" reason=%q", truncateForLog(payload.Reason, 120))
		}
		return line
	case agent.EventApprovalResolved:
		payload, ok := evt.Payload.(agent.ApprovalResolvedPayload)
		if !ok {
			return prefix
		}
		return fmt.Sprintf("%s iteration=%d tool=%s approved=%t",
			prefix, evt.Meta.Iteration, payload.Tool, payload.Approved)
	case agent.EventError:
		payload, ok := evt.Payload.(agent.ErrorPayload)
		if !ok {
			return prefix
		}
		return fmt.Sprintf("%s iteration=%d stage=%s message=%q",
			prefix, evt.Meta.Iteration, payload.Stage, truncateForLog(payload.Message, 120))
	case agent.EventTurnFinished:
		payload, ok := evt.Payload.(agent.TurnFinishedPayload)
		if !ok {
			return prefix
		}
		return fmt.Sprintf("%s status=%s final_len=%d", prefix, payload.Status, len(payload.FinalContent))
	case agent.EventModelReasoning:
		payload, ok := evt.Payload.(agent.ModelReasoningPayload)
		if !ok {
			return prefix
		}
		return fmt.Sprintf("%s iteration=%d delta=%q",
			prefix, evt.Meta.Iteration, truncateForLog(payload.Delta, 120))
	default:
		return ""
	}
}

func formatToolFinishedSummary(payload agent.ToolFinishedPayload) string {
	status := "status=ok"
	if payload.IsError {
		status = "status=error"
	}

	parts := []string{status}
	if payload.ErrorText != "" {
		parts = append(parts, fmt.Sprintf("error=%q", truncateForLog(payload.ErrorText, 120)))
	}
	if exitCode, ok := intMetadata(payload.Metadata, "exit_code"); ok {
		parts = append(parts, fmt.Sprintf("exit_code=%d", exitCode))
	}
	if timedOut, ok := boolMetadata(payload.Metadata, "timed_out"); ok {
		parts = append(parts, fmt.Sprintf("timed_out=%t", timedOut))
	}
	if outputBytes, ok := intMetadata(payload.Metadata, "output_bytes"); ok {
		parts = append(parts, fmt.Sprintf("output_bytes=%d", outputBytes))
	}
	if truncated, ok := boolMetadata(payload.Metadata, "truncated"); ok {
		parts = append(parts, fmt.Sprintf("truncated=%t", truncated))
	}
	if sample, ok := stringMetadata(payload.Metadata, "output_sample"); ok && sample != "" {
		parts = append(parts, fmt.Sprintf("sample=%q", truncateForLog(sample, 120)))
	} else if payload.ForModel != "" {
		parts = append(parts, fmt.Sprintf("output_length=%d for_model=%q", len(payload.ForModel), truncateForLog(payload.ForModel, 120)))
	} else if payload.UserPreview != "" {
		parts = append(parts, fmt.Sprintf("preview=%q", truncateForLog(payload.UserPreview, 80)))
	}
	return strings.Join(parts, " ")
}

func formatArgs(args map[string]any) string {
	if len(args) == 0 {
		return "{}"
	}

	keys := make([]string, 0, len(args))
	for key := range args {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", key, formatArgValue(args[key])))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func formatArgValue(value any) string {
	switch v := value.(type) {
	case string:
		return fmt.Sprintf("%q", truncateForLog(v, 60))
	default:
		return fmt.Sprintf("%v", v)
	}
}

func truncateForLog(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}

func intMetadata(metadata map[string]any, key string) (int, bool) {
	if metadata == nil {
		return 0, false
	}
	switch value := metadata[key].(type) {
	case int:
		return value, true
	case int64:
		return int(value), true
	case float64:
		return int(value), true
	default:
		return 0, false
	}
}

func boolMetadata(metadata map[string]any, key string) (bool, bool) {
	if metadata == nil {
		return false, false
	}
	value, ok := metadata[key].(bool)
	return value, ok
}

func stringMetadata(metadata map[string]any, key string) (string, bool) {
	if metadata == nil {
		return "", false
	}
	value, ok := metadata[key].(string)
	return value, ok
}
