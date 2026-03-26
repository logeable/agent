package profile

import (
	"os"
	"path/filepath"
	"testing"
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
system_prompt = "Be precise."
workdir = "."
max_iterations = 12

[files]
scope = "workspace"

[tools]
enabled = ["read_file", "bash"]

[tools.read_file]
max_bytes = 2048

[tools.bash]
timeout_ms = 1200
max_output_bytes = 4096
shell = "/bin/sh"
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
	if loop.Context.SystemPrompt != "Be precise." {
		t.Fatalf("SystemPrompt = %q, want %q", loop.Context.SystemPrompt, "Be precise.")
	}
	if loop.MaxIterations != 12 {
		t.Fatalf("MaxIterations = %d, want 12", loop.MaxIterations)
	}
	if len(loop.Tools.Definitions()) != 2 {
		t.Fatalf("tool definitions = %d, want 2", len(loop.Tools.Definitions()))
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
		Agent: AgentConfig{
			WorkDir: t.TempDir(),
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
