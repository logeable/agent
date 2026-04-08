package agentclirun

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	"github.com/logeable/agent/internal/agentclirender"
	"github.com/logeable/agent/pkg/agentcore/agent"
	"github.com/logeable/agent/pkg/agentcore/tooling"
	"github.com/logeable/agent/pkg/orchestration"
	"golang.org/x/term"
)

// SingleRunOptions controls the non-interactive one-shot execution path.
type SingleRunOptions struct {
	SessionKey              string
	Message                 string
	Stream                  bool
	ShowReasoning           bool
	ShowEvents              bool
	ShowOrchestrationEvents bool
	AutoApprove             bool
	RenderMarkdown          bool
}

// RunSingleMessage executes one user message and prints its output directly to
// the terminal. It returns a process-style exit code plus any terminal-visible
// error that should be reported by the caller.
func RunSingleMessage(loop *agent.Loop, orchestrationEvents *orchestration.EventBus, opts SingleRunOptions) (int, error) {
	loop.Approval = BuildCLIApprovalHandler(opts.AutoApprove)

	stopStreaming := startStreamingPrinter(loop.Events, opts.Stream, opts.ShowReasoning)
	stopEvents := startEventPrinter(loop.Events, opts.ShowEvents)
	stopOrchestrationEvents := startOrchestrationEventPrinter(orchestrationEvents, opts.ShowOrchestrationEvents)
	defer stopEvents()
	defer stopOrchestrationEvents()

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
		fmt.Println(renderAssistantMarkdown(resp, opts.RenderMarkdown))
	}
	return 0, nil
}

func renderAssistantMarkdown(content string, enabled bool) string {
	if !enabled {
		return strings.TrimSpace(content)
	}
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return strings.TrimSpace(content)
	}

	width, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || width <= 0 {
		return agentclirender.RenderMarkdown(content, 0)
	}
	renderWidth := width - 2
	if renderWidth < 20 {
		renderWidth = 20
	}
	return agentclirender.RenderMarkdown(content, renderWidth)
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

func startStreamingPrinter(events *agent.EventBus, enabled bool, showReasoning bool) func() bool {
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
				if !showReasoning {
					continue
				}
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
