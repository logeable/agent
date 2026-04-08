package agentclirun

import (
	"fmt"
	"os"
	"strings"

	"github.com/logeable/agent/pkg/orchestration"
)

func FormatOrchestrationEventLine(evt orchestration.Event) string {
	prefix := fmt.Sprintf("[%s]", evt.Kind)
	payload, _ := evt.Payload.(map[string]any)
	switch evt.Kind {
	case orchestration.EventChildStarted:
		return fmt.Sprintf("%s task=%v session=%v depth=%v",
			prefix, payload["task_index"], payload["session_key"], payload["depth"])
	case orchestration.EventChildFinished:
		line := fmt.Sprintf("%s task=%v status=%v session=%v",
			prefix, payload["task_index"], payload["status"], payload["session_key"])
		if files, ok := payload["output_files"].([]string); ok && len(files) > 0 {
			line += fmt.Sprintf(" output_files=%s", strings.Join(files, ","))
		}
		if errText, _ := payload["error"].(string); errText != "" {
			line += fmt.Sprintf(" error=%q", truncateForLog(errText, 120))
		}
		return line
	case orchestration.EventJobStarted:
		return fmt.Sprintf("%s job=%v session=%v", prefix, payload["job_id"], payload["session_key"])
	case orchestration.EventJobFinished:
		line := fmt.Sprintf("%s job=%v success=%v attempts=%v", prefix, payload["job_id"], payload["success"], payload["attempts"])
		if errText, _ := payload["error"].(string); errText != "" {
			line += fmt.Sprintf(" error=%q", truncateForLog(errText, 120))
		}
		return line
	case orchestration.EventCodeExecStarted:
		return fmt.Sprintf("%s workdir=%v", prefix, payload["workdir"])
	case orchestration.EventCodeExecFinished:
		line := fmt.Sprintf("%s tool_calls=%v", prefix, payload["tool_calls"])
		if errText, _ := payload["error"].(string); errText != "" {
			line += fmt.Sprintf(" error=%q", truncateForLog(errText, 120))
		}
		return line
	default:
		return ""
	}
}

func startOrchestrationEventPrinter(events *orchestration.EventBus, enabled bool) func() {
	if !enabled || events == nil {
		return func() {}
	}

	sub := events.Subscribe(64)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for evt := range sub.C {
			line := FormatOrchestrationEventLine(evt)
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
