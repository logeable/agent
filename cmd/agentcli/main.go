package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/logeable/agent/internal/agentapp"
	"github.com/logeable/agent/internal/agentclirun"
	"github.com/logeable/agent/internal/agentclitui"
	"github.com/logeable/agent/pkg/agentcore/agent"
	"github.com/logeable/agent/pkg/orchestration"
)

// This command is the smallest runnable terminal application for the extracted
// agent runtime using a real model provider.
//
// Why:
// This repository is focused on a stable execution core instead of demo-only
// scaffolding. The CLI therefore wires together:
// - the extracted Loop
// - the in-memory session store
// - the built-in file, shell, and web tools
// - a real OpenAI-compatible model provider
func main() {
	var (
		message          string
		sessionKey       string
		profileName      string
		providerKind     string
		modelName        string
		baseURL          string
		apiKey           string
		stream           bool
		showReasoning    bool
		showReasoningSet bool
		showEvents       bool
		showOrchEvents   bool
		useRuntime       bool
		delegateHint     bool
		delegateRequired bool
		autoApprove      bool
		renderMarkdown   bool
	)

	flag.StringVar(&message, "m", "", "Process a single message and exit")
	flag.StringVar(&sessionKey, "session", "agentcli:default", "Session key used to preserve conversation state")
	flag.StringVar(&profileName, "profile", "agent", "Profile name or path")
	flag.StringVar(&providerKind, "provider", "", "Provider kind to use: openai, openai_response, or ollama")
	flag.StringVar(&modelName, "model", "", "Model name for the selected provider")
	flag.StringVar(&baseURL, "base-url", "", "Base URL for the selected provider")
	flag.StringVar(&apiKey, "api-key", "", "API key for the selected provider")
	flag.BoolVar(&stream, "stream", true, "Render model delta events when the provider supports streaming")
	flag.Func("show-reasoning", "Show streamed reasoning when the provider emits it", func(value string) error {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return err
		}
		showReasoning = parsed
		showReasoningSet = true
		return nil
	})
	flag.BoolVar(&showEvents, "events", false, "Show key runtime events")
	flag.BoolVar(&showOrchEvents, "show-orchestration-events", false, "Show orchestration runtime events such as child delegation lifecycle")
	flag.BoolVar(&useRuntime, "runtime", false, "Build the orchestration runtime instead of the plain loop")
	flag.BoolVar(&delegateHint, "delegate-hint", false, "Append a delegation-planning hint for this runtime execution without hardcoding specific subtasks")
	flag.BoolVar(&delegateRequired, "delegate-required", false, "Require this run to use delegate_task at least once; injects a strong temporary delegation system prompt")
	flag.BoolVar(&autoApprove, "auto-approve", false, "Automatically approve tool approval requests")
	flag.BoolVar(&renderMarkdown, "render-markdown", true, "Render assistant messages as Markdown in terminal views")
	flag.Parse()

	loopOpts := agentapp.LoopOptions{
		ProfileName:  profileName,
		ProviderKind: providerKind,
		ModelName:    modelName,
		BaseURL:      baseURL,
		APIKey:       apiKey,
	}
	var (
		loop    *agent.Loop
		runtime *agentapp.Runtime
		err     error
	)
	if useRuntime || delegateHint || delegateRequired {
		runtime, err = agentapp.BuildRuntime(loopOpts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		defer runtime.Close()
		loop = runtime.Loop
	} else {
		loop, err = agentapp.BuildLoop(loopOpts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		defer loop.Close()
	}
	loop.DisableStreaming = !stream
	loop.Events = agent.NewEventBus()
	defer loop.Events.Close()
	stdinMessage, err := readMessageFromStdin(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if strings.TrimSpace(stdinMessage) != "" {
		if strings.TrimSpace(message) != "" {
			message = strings.TrimSpace(message) + "\n\n" + strings.TrimSpace(stdinMessage)
		} else {
			message = stdinMessage
		}
	}
	if delegateHint {
		message = buildDelegationHintMessage(message)
	}
	if delegateRequired {
		loop.Context.SystemPrompt = appendTemporarySystemPrompt(loop.Context.SystemPrompt, buildDelegationRequiredPrompt())
	}

	if strings.TrimSpace(message) != "" {
		if !showReasoningSet {
			showReasoning = false
		}
		var orchestrationEvents *orchestration.EventBus
		if runtime != nil {
			orchestrationEvents = runtime.Events
		}
		exitCode, err := agentclirun.RunSingleMessage(loop, orchestrationEvents, agentclirun.SingleRunOptions{
			SessionKey:              sessionKey,
			Message:                 message,
			Stream:                  stream,
			ShowReasoning:           showReasoning,
			ShowEvents:              showEvents,
			ShowOrchestrationEvents: showOrchEvents,
			AutoApprove:             autoApprove,
			RenderMarkdown:          renderMarkdown,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(exitCode)
		}
		return
	}

	if !showReasoningSet {
		showReasoning = true
	}

	if err := agentclitui.Run(loop, agentclitui.Options{
		SessionKey:     sessionKey,
		ProfileName:    agentapp.DisplayProfileName(profileName),
		Stream:         stream,
		ShowReasoning:  showReasoning,
		ShowEvents:     showEvents,
		AutoApprove:    autoApprove,
		RenderMarkdown: renderMarkdown,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func buildDelegationHintMessage(base string) string {
	hint := strings.TrimSpace(`
Delegation hint for this run:
If the task is complex enough to benefit from subagents, first form a short delegation plan before calling delegate_task.
Only delegate isolated reasoning-heavy subtasks or independent workstreams.
When delegating, include concrete paths, errors, constraints, and any other missing context the child needs because child workers cannot see the parent conversation history.
If the task does not benefit from delegation, continue normally without using delegate_task.
`)
	if strings.TrimSpace(base) == "" {
		return hint
	}
	return strings.TrimSpace(base) + "\n\n" + hint
}

func buildDelegationRequiredPrompt() string {
	return strings.TrimSpace(`
# Delegation Required
This run must use delegate_task at least once before you deliver the final answer.
First form a short delegation plan, then call delegate_task for at least one isolated reasoning-heavy subtask or one batch of independent subtasks.
Do not complete the entire task yourself without delegating.
When delegating, pass concrete paths, errors, constraints, and any other missing context because child workers cannot see the parent conversation history.
After delegated work returns, synthesize the child summaries into the final response.
`)
}

func appendTemporarySystemPrompt(base, extra string) string {
	base = strings.TrimSpace(base)
	extra = strings.TrimSpace(extra)
	switch {
	case base == "":
		return extra
	case extra == "":
		return base
	default:
		return base + "\n\n---\n\n" + extra
	}
}

func readMessageFromStdin(stdin *os.File) (string, error) {
	if stdin == nil {
		return "", nil
	}

	info, err := stdin.Stat()
	if err != nil {
		return "", fmt.Errorf("could not inspect stdin: %w", err)
	}
	if info.Mode()&os.ModeCharDevice != 0 {
		return "", nil
	}

	data, err := io.ReadAll(stdin)
	if err != nil {
		return "", fmt.Errorf("could not read stdin: %w", err)
	}
	return string(data), nil
}

func resolveProfileArgument(raw string) (string, error) {
	return agentapp.ResolveProfileArgument(raw)
}

func resolveProfileArgumentWithHome(value, homeDir string) string {
	return agentapp.ResolveProfileArgumentWithHomeForTests(value, homeDir)
}
