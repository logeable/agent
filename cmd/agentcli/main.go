package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/logeable/agent/internal/agentclirun"
	"github.com/logeable/agent/internal/agentclitui"
	"github.com/logeable/agent/pkg/agentcore/agent"
	"github.com/logeable/agent/pkg/profile"
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
		message      string
		sessionKey   string
		profileName  string
		providerKind string
		modelName    string
		baseURL      string
		apiKey       string
		stream       bool
		showEvents   bool
		autoApprove  bool
	)

	flag.StringVar(&message, "m", "", "Process a single message and exit")
	flag.StringVar(&sessionKey, "session", "agentcli:default", "Session key used to preserve conversation state")
	flag.StringVar(&profileName, "profile", "agent", "Profile name or path")
	flag.StringVar(&providerKind, "provider", "", "Provider kind to use: openai or openai_response")
	flag.StringVar(&modelName, "model", "", "Model name for the OpenAI-compatible provider")
	flag.StringVar(&baseURL, "base-url", "", "Base URL for OpenAI-compatible providers")
	flag.StringVar(&apiKey, "api-key", "", "API key for OpenAI-compatible providers")
	flag.BoolVar(&stream, "stream", true, "Render model delta events when the provider supports streaming")
	flag.BoolVar(&showEvents, "events", false, "Show key runtime events")
	flag.BoolVar(&autoApprove, "auto-approve", false, "Automatically approve tool approval requests")
	flag.Parse()

	loop, err := buildLoop(profileName, providerKind, modelName, baseURL, apiKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer loop.Close()
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
		exitCode, err := agentclirun.RunSingleMessage(loop, agentclirun.SingleRunOptions{
			SessionKey:  sessionKey,
			Message:     message,
			Stream:      stream,
			ShowEvents:  showEvents,
			AutoApprove: autoApprove,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(exitCode)
		}
		return
	}

	if err := agentclitui.Run(loop, agentclitui.Options{
		SessionKey:  sessionKey,
		Stream:      stream,
		ShowEvents:  showEvents,
		AutoApprove: autoApprove,
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

func buildLoop(profileName, providerKind, modelName, baseURL, apiKey string) (*agent.Loop, error) {
	cfgPath, err := resolveProfileArgument(profileName)
	if err != nil {
		return nil, err
	}
	if cfgPath != "" {
		cfg, err := profile.Load(cfgPath)
		if err != nil {
			return nil, err
		}
		return cfg.BuildLoop(profile.BuildOptions{
			ProviderKind: providerKind,
			BaseURL:      baseURL,
			APIKey:       apiKey,
			Model:        modelName,
		})
	}

	if _, err := os.Stat("agent.toml"); err == nil {
		cfg, err := profile.Load("agent.toml")
		if err != nil {
			return nil, err
		}
		return cfg.BuildLoop(profile.BuildOptions{
			ProviderKind: providerKind,
			BaseURL:      baseURL,
			APIKey:       apiKey,
			Model:        modelName,
		})
	}

	cfg, err := defaultCLIProfile()
	if err != nil {
		return nil, err
	}
	return cfg.BuildLoop(profile.BuildOptions{
		ProviderKind: providerKind,
		BaseURL:      baseURL,
		APIKey:       apiKey,
		Model:        modelName,
	})
}

func resolveProfileArgument(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not resolve home directory for profile lookup: %w", err)
	}
	return resolveProfileArgumentWithHome(value, homeDir), nil
}

func resolveProfileArgumentWithHome(value, homeDir string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}

	// Names are the ergonomic default for day-to-day CLI use. We only treat
	// simple values as names; anything with a path separator is assumed to be a
	// real path and skips the name lookup.
	if !filepath.IsAbs(trimmed) && !strings.ContainsRune(trimmed, filepath.Separator) {
		name := strings.TrimSuffix(trimmed, ".toml")
		if homeDir != "" {
			candidate := filepath.Join(homeDir, ".agentcli", name+".toml")
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
		}
	}

	return trimmed
}

func defaultCLIProfile() (*profile.Config, error) {
	return &profile.Config{
		Name: "agentcli",
		Provider: profile.ProviderConfig{
			Kind: "openai",
		},
		Agent: profile.AgentConfig{
			ID:            "agentcli",
			Identity:      profile.BuildDefaultIdentity(),
			Soul:          profile.BuildDefaultSoul(),
			MaxIterations: agent.DefaultMaxIterations,
		},
		Tools: profile.ToolsConfig{
			Enabled: []string{"read_file", "edit_file", "write_file", "bash", "web_fetch"},
			ReadFile: profile.ReadFileToolConfig{
				MaxBytes: 128 * 1024,
			},
			Bash: profile.BashToolConfig{
				TimeoutMS:      30_000,
				MaxOutputBytes: 64 * 1024,
			},
			WebFetch: profile.WebFetchToolConfig{
				TimeoutMS: 20_000,
				MaxBytes:  128 * 1024,
			},
		},
	}, nil
}
