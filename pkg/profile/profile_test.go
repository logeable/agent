package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/logeable/agent/pkg/agentcore/provider"
)

func TestLoadAndBuildLoop(t *testing.T) {
	tempDir := t.TempDir()
	profilePath := filepath.Join(tempDir, "coding.toml")
	err := os.WriteFile(profilePath, []byte(`
name = "coding"

[provider]
kind = "openai"
api_key = "dummy-key"
model = "gpt-5"

[agent]
id = "coding-agent"
identity = "You are precise."
soul = "Prefer inspection before changes."
max_iterations = 12

[files]
scope = "workspace"

[tools]
enabled = ["read_file", "bash", "web_fetch"]

[tools.read_file]
max_bytes = 2048

[tools.bash]
timeout_ms = 1200
max_output_bytes = 4096
shell = "/bin/sh"

[tools.web_fetch]
timeout_ms = 1500
max_bytes = 8192
`), 0o644)
	if err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := Load(profilePath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	loop, err := cfg.BuildLoop(BuildOptions{})
	if err != nil {
		t.Fatalf("BuildLoop() error = %v", err)
	}

	if loop.AgentID != "coding-agent" {
		t.Fatalf("AgentID = %q, want %q", loop.AgentID, "coding-agent")
	}
	if loop.ModelName != "gpt-5" {
		t.Fatalf("ModelName = %q, want %q", loop.ModelName, "gpt-5")
	}
	if !strings.Contains(loop.Context.SystemPrompt, "You are precise.") {
		t.Fatalf("SystemPrompt missing identity, got %q", loop.Context.SystemPrompt)
	}
	if !strings.Contains(loop.Context.SystemPrompt, "Prefer inspection before changes.") {
		t.Fatalf("SystemPrompt missing soul, got %q", loop.Context.SystemPrompt)
	}
	if !strings.Contains(loop.Context.SystemPrompt, "# Environment") {
		t.Fatalf("SystemPrompt missing environment section, got %q", loop.Context.SystemPrompt)
	}
	if loop.MaxIterations != 12 {
		t.Fatalf("MaxIterations = %d, want 12", loop.MaxIterations)
	}
	if len(loop.Tools.Definitions()) != 3 {
		t.Fatalf("tool definitions = %d, want 3", len(loop.Tools.Definitions()))
	}
}

func TestBuildSystemPromptUsesDefaults(t *testing.T) {
	prompt := BuildSystemPrompt("", "", EnvironmentInfo{
		WorkDir:      "/tmp/project",
		FilesScope:   "workspace",
		EnabledTools: []string{"read_file", "bash"},
	})

	if !strings.Contains(prompt, "# Identity") {
		t.Fatalf("prompt missing identity section: %q", prompt)
	}
	if !strings.Contains(prompt, "# Soul") {
		t.Fatalf("prompt missing soul section: %q", prompt)
	}
	if !strings.Contains(prompt, "/tmp/project") {
		t.Fatalf("prompt missing workdir: %q", prompt)
	}
}

func TestValidateRejectsUnknownTool(t *testing.T) {
	cfg := &Config{
		Provider: ProviderConfig{
			Kind: "openai",
		},
		Tools: ToolsConfig{
			Enabled: []string{"read_file", "unknown"},
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want non-nil")
	}
}

func TestBuildLoopAllowsModelFromOverride(t *testing.T) {
	cfg := &Config{
		Provider: ProviderConfig{
			Kind:   "openai",
			APIKey: "dummy-key",
		},
	}

	loop, err := cfg.BuildLoop(BuildOptions{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("BuildLoop() error = %v", err)
	}
	if loop.ModelName != "gpt-5" {
		t.Fatalf("ModelName = %q, want %q", loop.ModelName, "gpt-5")
	}
}

func TestBuildLoopAllowsProviderKindOverride(t *testing.T) {
	cfg := &Config{
		Provider: ProviderConfig{
			Kind:   "openai",
			APIKey: "dummy-key",
			Model:  "gpt-5",
		},
	}

	loop, err := cfg.BuildLoop(BuildOptions{
		ProviderKind: "openai_response",
		BaseURL:      "https://api.openai.com/v1",
	})
	if err != nil {
		t.Fatalf("BuildLoop() error = %v", err)
	}
	if _, ok := loop.Model.(*provider.OpenAIResponseModel); !ok {
		t.Fatalf("loop.Model = %T, want *provider.OpenAIResponseModel", loop.Model)
	}
}

func TestValidateRejectsExplicitFilesWithoutRoots(t *testing.T) {
	cfg := &Config{
		Provider: ProviderConfig{
			Kind: "openai",
		},
		Files: FilesConfig{
			Scope: "explicit",
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want non-nil")
	}
}

func TestValidateRejectsNegativeMaxIterations(t *testing.T) {
	cfg := &Config{
		Provider: ProviderConfig{
			Kind: "openai",
		},
		Agent: AgentConfig{
			MaxIterations: -1,
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want non-nil")
	}
}
