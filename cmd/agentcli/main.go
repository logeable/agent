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
	flag.BoolVar(&autoApprove, "auto-approve", false, "Automatically approve tool approval requests")
	flag.BoolVar(&renderMarkdown, "render-markdown", true, "Render assistant messages as Markdown in terminal views")
	flag.Parse()

	loop, err := agentapp.BuildLoop(agentapp.LoopOptions{
		ProfileName:  profileName,
		ProviderKind: providerKind,
		ModelName:    modelName,
		BaseURL:      baseURL,
		APIKey:       apiKey,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer loop.Close()
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

	if strings.TrimSpace(message) != "" {
		if !showReasoningSet {
			showReasoning = false
		}
		exitCode, err := agentclirun.RunSingleMessage(loop, agentclirun.SingleRunOptions{
			SessionKey:     sessionKey,
			Message:        message,
			Stream:         stream,
			ShowReasoning:  showReasoning,
			ShowEvents:     showEvents,
			AutoApprove:    autoApprove,
			RenderMarkdown: renderMarkdown,
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
