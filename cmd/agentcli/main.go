package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	"github.com/logeable/agent/internal/demo"
	"github.com/logeable/agent/pkg/agentcore/agent"
	"github.com/logeable/agent/pkg/agentcore/provider"
	"github.com/logeable/agent/pkg/agentcore/session"
	"github.com/logeable/agent/pkg/agentcore/tools"
)

// This command is the smallest runnable terminal application for the extracted
// agent runtime.
//
// Why:
// A beginner-friendly repository should let readers run something concrete as
// early as possible. This CLI wires together:
// - the extracted Loop
// - the in-memory session store
// - two tiny demo tools
// - a fake model that can request tools
//
// Later we can swap the fake model for a real provider without changing most
// of the rest of the control flow.
func main() {
	var (
		message      string
		sessionKey   string
		providerName string
		modelName    string
		baseURL      string
		apiKey       string
		stream       bool
	)

	flag.StringVar(&message, "m", "", "Process a single message and exit")
	flag.StringVar(&sessionKey, "session", "demo:default", "Session key used to preserve conversation state")
	flag.StringVar(&providerName, "provider", "demo", "Provider to use: demo or openai")
	flag.StringVar(&modelName, "model", "", "Model name for real providers")
	flag.StringVar(&baseURL, "base-url", "", "Base URL for OpenAI-compatible providers")
	flag.StringVar(&apiKey, "api-key", "", "API key for OpenAI-compatible providers")
	flag.BoolVar(&stream, "stream", true, "Render model delta events when the provider supports streaming")
	flag.Parse()

	loop, err := buildLoop(providerName, modelName, baseURL, apiKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	loop.Events = agent.NewEventBus()
	defer loop.Events.Close()

	if strings.TrimSpace(message) != "" {
		runSingleMessage(loop, sessionKey, message, stream)
		return
	}

	runInteractive(loop, sessionKey, stream)
}

func buildLoop(providerName, modelName, baseURL, apiKey string) (*agent.Loop, error) {
	registry := tools.NewRegistry()
	registry.Register(demo.EchoTool{})
	registry.Register(demo.TimeTool{})

	chatModel, resolvedModelName, err := buildModel(providerName, modelName, baseURL, apiKey)
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

func buildModel(providerName, modelName, baseURL, apiKey string) (provider.ChatModel, string, error) {
	switch strings.ToLower(strings.TrimSpace(providerName)) {
	case "", "demo":
		return &demo.RuleBasedModel{}, "demo-rule-model", nil
	case "openai":
		resolvedBaseURL := firstNonEmpty(baseURL, os.Getenv("OPENAI_BASE_URL"), "https://api.openai.com/v1")
		resolvedAPIKey := firstNonEmpty(apiKey, os.Getenv("OPENAI_API_KEY"))
		resolvedModel := firstNonEmpty(modelName, os.Getenv("OPENAI_MODEL"))

		if resolvedAPIKey == "" {
			return nil, "", fmt.Errorf("OPENAI_API_KEY or -api-key is required when -provider=openai")
		}
		if resolvedModel == "" {
			return nil, "", fmt.Errorf("OPENAI_MODEL or -model is required when -provider=openai")
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
	default:
		return nil, "", fmt.Errorf("unknown provider %q", providerName)
	}
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

	fmt.Println("agentcli interactive demo")
	fmt.Println("Type a message and press Enter. Type `exit` or `quit` to stop.")
	fmt.Println("Examples: `what time is it?`, `echo hello world`")

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
