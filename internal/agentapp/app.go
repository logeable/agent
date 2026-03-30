package agentapp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/logeable/agent/pkg/agentcore/agent"
	"github.com/logeable/agent/pkg/profile"
)

type LoopOptions struct {
	ProfileName  string
	ProviderKind string
	ModelName    string
	BaseURL      string
	APIKey       string
	WorkDir      string
}

func BuildLoop(opts LoopOptions) (*agent.Loop, error) {
	cfgPath, err := ResolveProfileArgument(opts.ProfileName)
	if err != nil {
		return nil, err
	}
	if cfgPath != "" {
		cfg, err := profile.Load(cfgPath)
		if err != nil {
			return nil, err
		}
		return cfg.BuildLoop(profile.BuildOptions{
			ProviderKind: opts.ProviderKind,
			BaseURL:      opts.BaseURL,
			APIKey:       opts.APIKey,
			Model:        opts.ModelName,
			WorkDir:      opts.WorkDir,
		})
	}

	if _, err := os.Stat("agent.toml"); err == nil {
		cfg, err := profile.Load("agent.toml")
		if err != nil {
			return nil, err
		}
		return cfg.BuildLoop(profile.BuildOptions{
			ProviderKind: opts.ProviderKind,
			BaseURL:      opts.BaseURL,
			APIKey:       opts.APIKey,
			Model:        opts.ModelName,
			WorkDir:      opts.WorkDir,
		})
	}

	cfg, err := DefaultCLIProfile()
	if err != nil {
		return nil, err
	}
	return cfg.BuildLoop(profile.BuildOptions{
		ProviderKind: opts.ProviderKind,
		BaseURL:      opts.BaseURL,
		APIKey:       opts.APIKey,
		Model:        opts.ModelName,
		WorkDir:      opts.WorkDir,
	})
}

func ResolveProfileArgument(raw string) (string, error) {
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

func ResolveProfileArgumentWithHomeForTests(value, homeDir string) string {
	return resolveProfileArgumentWithHome(value, homeDir)
}

func resolveProfileArgumentWithHome(value, homeDir string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}

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

func DisplayProfileName(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if filepath.IsAbs(value) || strings.ContainsRune(value, filepath.Separator) {
		base := filepath.Base(value)
		return strings.TrimSuffix(base, filepath.Ext(base))
	}
	return strings.TrimSuffix(value, ".toml")
}

func DefaultCLIProfile() (*profile.Config, error) {
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
				MaxBytes: 32 * 1024,
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
