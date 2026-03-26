package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/logeable/agent/pkg/agentcore/agent"
	"github.com/logeable/agent/pkg/agentcore/provider"
	"github.com/logeable/agent/pkg/agentcore/session"
	"github.com/logeable/agent/pkg/agentcore/tooling"
	builtintools "github.com/logeable/agent/pkg/tools"
)

// This command is the smallest runnable terminal application for the extracted
// agent runtime using a real model provider.
//
// Why:
// This repository is focused on a stable execution core instead of demo-only
// scaffolding. The CLI therefore wires together:
// - the extracted Loop
// - the in-memory session store
// - the built-in file and shell tools
// - a real OpenAI-compatible model provider
func main() {
	var (
		message    string
		sessionKey string
		modelName  string
		baseURL    string
		apiKey     string
		stream     bool
		showEvents bool
	)

	flag.StringVar(&message, "m", "", "Process a single message and exit")
	flag.StringVar(&sessionKey, "session", "agentcli:default", "Session key used to preserve conversation state")
	flag.StringVar(&modelName, "model", "", "Model name for the OpenAI-compatible provider")
	flag.StringVar(&baseURL, "base-url", "", "Base URL for OpenAI-compatible providers")
	flag.StringVar(&apiKey, "api-key", "", "API key for OpenAI-compatible providers")
	flag.BoolVar(&stream, "stream", true, "Render model delta events when the provider supports streaming")
	flag.BoolVar(&showEvents, "events", true, "Print key runtime events to stderr")
	flag.Parse()

	loop, err := buildLoop(modelName, baseURL, apiKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	loop.Events = agent.NewEventBus()
	defer loop.Events.Close()
	stopEvents := startEventPrinter(loop.Events, showEvents)
	defer stopEvents()

	if strings.TrimSpace(message) != "" {
		runSingleMessage(loop, sessionKey, message, stream)
		return
	}

	runInteractive(loop, sessionKey, stream)
}

func buildLoop(modelName, baseURL, apiKey string) (*agent.Loop, error) {
	registry := tooling.NewRegistry()

	workDir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("could not resolve current working directory: %w", err)
	}
	registry.Register(builtintools.ReadFileTool{
		RootDir:  workDir,
		MaxBytes: 128 * 1024,
	})
	registry.Register(builtintools.WriteFileTool{
		RootDir: workDir,
	})
	registry.Register(builtintools.EditFileTool{
		RootDir: workDir,
	})
	registry.Register(builtintools.BashTool{
		WorkDir:        workDir,
		Timeout:        30 * time.Second,
		MaxOutputBytes: 64 * 1024,
	})

	chatModel, resolvedModelName, err := buildModel(modelName, baseURL, apiKey)
	if err != nil {
		return nil, err
	}

	return &agent.Loop{
		Model:     chatModel,
		ModelName: resolvedModelName,
		Tools:     registry,
		Sessions:  session.NewMemoryStore(),
		Context: agent.ContextBuilder{
			SystemPrompt: "You are a tiny teaching agent. Use tools when needed, and keep answers short.",
		},
	}, nil
}

func buildModel(modelName, baseURL, apiKey string) (provider.ChatModel, string, error) {
	resolvedBaseURL := firstNonEmpty(baseURL, os.Getenv("OPENAI_BASE_URL"), "https://api.openai.com/v1")
	resolvedAPIKey := firstNonEmpty(apiKey, os.Getenv("OPENAI_API_KEY"))
	resolvedModel := firstNonEmpty(modelName, os.Getenv("OPENAI_MODEL"))

	if resolvedAPIKey == "" {
		return nil, "", fmt.Errorf("OPENAI_API_KEY or -api-key is required")
	}
	if resolvedModel == "" {
		return nil, "", fmt.Errorf("OPENAI_MODEL or -model is required")
	}

	model, err := provider.NewOpenAICompatModel(provider.OpenAICompatConfig{
		BaseURL: resolvedBaseURL,
		APIKey:  resolvedAPIKey,
		Model:   resolvedModel,
	})
	if err != nil {
		return nil, "", err
	}
	return model, resolvedModel, nil
}

func runSingleMessage(loop *agent.Loop, sessionKey, message string, stream bool) {
	stopStreaming := startStreamingPrinter(loop.Events, stream)
	resp, err := loop.Process(context.Background(), sessionKey, message)
	if err != nil {
		stopStreaming()
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if !stopStreaming() {
		fmt.Println(resp)
	}
}

func runInteractive(loop *agent.Loop, sessionKey string, stream bool) {
	scanner := bufio.NewScanner(os.Stdin)

	fmt.Println("agentcli interactive mode")
	fmt.Println("Type a message and press Enter. Type `exit` or `quit` to stop.")
	fmt.Println("Examples: `read go.mod`, `show files under pkg`, `run go test ./...`")

	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			fmt.Println()
			return
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == "exit" || line == "quit" {
			return
		}

		stopStreaming := startStreamingPrinter(loop.Events, stream)
		resp, err := loop.Process(context.Background(), sessionKey, line)
		if err != nil {
			stopStreaming()
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			continue
		}
		if !stopStreaming() {
			fmt.Printf("%s\n", resp)
		}
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
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
			if evt.Kind != agent.EventModelDelta {
				continue
			}
			payload, ok := evt.Payload.(agent.ModelDeltaPayload)
			if !ok || payload.Delta == "" {
				continue
			}
			printedAny.Store(true)
			fmt.Print(payload.Delta)
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
			line := formatEventLine(evt)
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

func formatEventLine(evt agent.Event) string {
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
