package agentclirun

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/logeable/agent/pkg/agentcore/agent"
	"github.com/logeable/agent/pkg/agentcore/tooling"
)

// SingleRunOptions controls the non-interactive one-shot execution path.
type SingleRunOptions struct {
	SessionKey  string
	Message     string
	Stream      bool
	ShowEvents  bool
	AutoApprove bool
}

// RunSingleMessage executes one user message and prints its output directly to
// the terminal. It returns a process-style exit code plus any terminal-visible
// error that should be reported by the caller.
func RunSingleMessage(loop *agent.Loop, opts SingleRunOptions) (int, error) {
	loop.Approval = BuildCLIApprovalHandler(opts.AutoApprove)

	stopStreaming := startStreamingPrinter(loop.Events, opts.Stream)
	stopEvents := startEventPrinter(loop.Events, opts.ShowEvents)
	defer stopEvents()

	resp, err := loop.Process(context.Background(), opts.SessionKey, opts.Message)
	if err != nil {
		stopStreaming()
		if deniedErr, ok := err.(*agent.ApprovalDeniedError); ok {
			return 2, fmt.Errorf("approval denied: %s", FormatDeniedError(deniedErr))
		}
		if approvalErr, ok := err.(*agent.ApprovalRequiredError); ok {
			return 2, fmt.Errorf("approval required: %s", FormatApprovalError(approvalErr))
		}
		return 1, err
	}
	if !stopStreaming() {
		fmt.Println(resp)
	}
	return 0, nil
}

// BuildCLIApprovalHandler returns the plain terminal approval flow used by the
// non-TUI command mode.
func BuildCLIApprovalHandler(autoApprove bool) agent.ApprovalHandler {
	if autoApprove {
		return func(_ context.Context, req tooling.ApprovalRequest) (bool, error) {
			fmt.Fprintf(os.Stderr, "auto-approved: %s\n", approvalSummary(req))
			return true, nil
		}
	}
	return promptForApprovalCLI
}

func promptForApprovalCLI(_ context.Context, req tooling.ApprovalRequest) (bool, error) {
	var line string

	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Approval required")
	if req.Tool != "" {
		fmt.Fprintf(os.Stderr, "Tool: %s\n", req.Tool)
	}
	if req.ActionLabel != "" {
		fmt.Fprintf(os.Stderr, "Action: %s\n", req.ActionLabel)
	}
	if req.Reason != "" {
		fmt.Fprintf(os.Stderr, "Reason: %s\n", req.Reason)
	}
	if len(req.Details) > 0 {
		if command, ok := req.Details["command"].(string); ok && command != "" {
			fmt.Fprintf(os.Stderr, "Command: %s\n", command)
		}
		if workdir, ok := req.Details["workdir"].(string); ok && workdir != "" {
			fmt.Fprintf(os.Stderr, "Workdir: %s\n", workdir)
		}
		if path, ok := req.Details["resolved_path"].(string); ok && path != "" {
			fmt.Fprintf(os.Stderr, "Path: %s\n", path)
		}
	}
	fmt.Fprint(os.Stderr, "Approve? [y/N]: ")
	if _, err := fmt.Fscanln(os.Stdin, &line); err != nil && !strings.Contains(err.Error(), "unexpected newline") {
		return false, err
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

func startStreamingPrinter(events *agent.EventBus, enabled bool) func() bool {
	if !enabled || events == nil {
		return func() bool { return false }
	}

	sub := events.Subscribe(32)
	var printedAny atomic.Bool
	stopped := make(chan struct{})

	go func() {
		defer close(stopped)
		for evt := range sub.C {
			switch evt.Kind {
			case agent.EventModelDelta:
				payload, ok := evt.Payload.(agent.ModelDeltaPayload)
				if !ok || payload.Delta == "" {
					continue
				}
				printedAny.Store(true)
				fmt.Print(payload.Delta)
			case agent.EventModelReasoning:
				payload, ok := evt.Payload.(agent.ModelReasoningPayload)
				if !ok || payload.Delta == "" {
					continue
				}
				// Reasoning belongs on stderr so stdout remains pipeline-friendly.
				fmt.Fprint(os.Stderr, payload.Delta)
			}
		}
	}()

	closed := false
	return func() bool {
		if !closed {
			events.Unsubscribe(sub.ID)
			<-stopped
			closed = true
		}
		if printedAny.Load() {
			fmt.Println()
		}
		return printedAny.Load()
	}
}

func startEventPrinter(events *agent.EventBus, enabled bool) func() {
	if !enabled || events == nil {
		return func() {}
	}

	sub := events.Subscribe(64)
	stopped := make(chan struct{})

	go func() {
		defer close(stopped)
		for evt := range sub.C {
			line := FormatEventLine(evt)
			if line == "" {
				continue
			}
			fmt.Fprintln(os.Stderr, line)
		}
	}()

	closed := false
	return func() {
		if closed {
			return
		}
		events.Unsubscribe(sub.ID)
		<-stopped
		closed = true
	}
}

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
	case agent.EventModelRequest:
		payload, ok := evt.Payload.(agent.ModelRequestPayload)
		if !ok {
			return prefix
		}
		return fmt.Sprintf("%s iteration=%d model=%s messages=%d tools=%d streaming=%t",
			prefix, evt.Meta.Iteration, payload.Model, payload.MessagesCount, payload.ToolsCount, payload.Streaming)
	case agent.EventModelResponse:
		payload, ok := evt.Payload.(agent.ModelResponsePayload)
		if !ok {
			return prefix
		}
		return fmt.Sprintf("%s iteration=%d content_len=%d tool_calls=%d",
			prefix, evt.Meta.Iteration, payload.ContentLen, payload.ToolCalls)
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

// FormatApprovalError renders a short approval-needed summary for terminal use.
func FormatApprovalError(err *agent.ApprovalRequiredError) string {
	if err == nil {
		return "approval required"
	}

	parts := make([]string, 0, 3)
	if err.Request.Tool != "" {
		parts = append(parts, fmt.Sprintf("tool=%s", err.Request.Tool))
	}
	if err.Request.ActionLabel != "" {
		parts = append(parts, fmt.Sprintf("action=%q", truncateForLog(err.Request.ActionLabel, 80)))
	}
	if err.Request.Reason != "" {
		parts = append(parts, fmt.Sprintf("reason=%q", truncateForLog(err.Request.Reason, 120)))
	}
	if len(parts) == 0 {
		return "approval required"
	}
	return strings.Join(parts, " ")
}

// FormatDeniedError renders a short approval-denied summary for terminal use.
func FormatDeniedError(err *agent.ApprovalDeniedError) string {
	if err == nil {
		return "approval denied"
	}
	if err.Request.ActionLabel != "" {
		return truncateForLog(err.Request.ActionLabel, 80)
	}
	if err.Request.Reason != "" {
		return truncateForLog(err.Request.Reason, 120)
	}
	return "approval denied"
}

func approvalSummary(req tooling.ApprovalRequest) string {
	parts := make([]string, 0, 3)
	if req.Tool != "" {
		parts = append(parts, fmt.Sprintf("tool=%s", req.Tool))
	}
	if req.ActionLabel != "" {
		parts = append(parts, fmt.Sprintf("action=%q", truncateForLog(req.ActionLabel, 80)))
	}
	if req.Reason != "" {
		parts = append(parts, fmt.Sprintf("reason=%q", truncateForLog(req.Reason, 120)))
	}
	if len(parts) == 0 {
		return "approval granted"
	}
	return strings.Join(parts, " ")
}
