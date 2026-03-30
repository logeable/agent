package profile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/logeable/agent/pkg/agentcore/provider"
)

func TestLoadAndBuildLoop(t *testing.T) {
	tempDir := t.TempDir()
	skillDir := filepath.Join(tempDir, "skills", "summarize")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: summarize
description: Summarize long material into concise notes.
---
# Summarize
`), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	profilePath := filepath.Join(tempDir, "coding.toml")
	err := os.WriteFile(profilePath, []byte(`
name = "coding"

[provider]
kind = "openai"
api_key = "dummy-key"
model = "gpt-5"

[provider.options]
reasoning_effort = "medium"
temperature = 0.2

[agent]
id = "coding-agent"
identity = "You are precise."
soul = "Prefer inspection before changes."
max_iterations = 12

[files]
scope = "workspace"

[skills]
enabled = true

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
	cfg.Skills.Roots = []string{filepath.Join(tempDir, "skills")}

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
	if !strings.Contains(loop.Context.SystemPrompt, "# Skills") {
		t.Fatalf("SystemPrompt missing skills section, got %q", loop.Context.SystemPrompt)
	}
	if !strings.Contains(loop.Context.SystemPrompt, "summarize") {
		t.Fatalf("SystemPrompt missing skill summary, got %q", loop.Context.SystemPrompt)
	}
	if loop.MaxIterations != 12 {
		t.Fatalf("MaxIterations = %d, want 12", loop.MaxIterations)
	}
	if got := loop.Options["reasoning_effort"]; got != "medium" {
		t.Fatalf("loop.Options[reasoning_effort] = %v, want medium", got)
	}
	if got := loop.Options["temperature"]; got != float64(0.2) {
		t.Fatalf("loop.Options[temperature] = %#v, want 0.2", got)
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
	}, "")

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

func TestBuildLoopSupportsOllamaWithoutAPIKey(t *testing.T) {
	cfg := &Config{
		Provider: ProviderConfig{
			Kind:  "ollama",
			Model: "qwen3",
		},
	}

	loop, err := cfg.BuildLoop(BuildOptions{})
	if err != nil {
		t.Fatalf("BuildLoop() error = %v", err)
	}
	if _, ok := loop.Model.(*provider.OllamaModel); !ok {
		t.Fatalf("loop.Model = %T, want *provider.OllamaModel", loop.Model)
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

func TestResolvedSkillRootsExpandsVariables(t *testing.T) {
	tempDir := t.TempDir()
	homeDir := filepath.Join(tempDir, "home")
	profileDir := filepath.Join(tempDir, "profiles")
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	t.Setenv("HOME", homeDir)

	cfg := &Config{
		Skills: SkillsConfig{
			Roots: []string{"${HOME}/skills", "${CWD}/local-skills", "${PROFILE_DIR}/bundle"},
		},
		sourcePath: filepath.Join(profileDir, "agent.toml"),
	}

	roots, err := cfg.resolvedSkillRoots("/ignored")
	if err != nil {
		t.Fatalf("resolvedSkillRoots() error = %v", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}

	want := []string{
		filepath.Join(homeDir, "skills"),
		filepath.Join(cwd, "local-skills"),
		filepath.Join(profileDir, "bundle"),
	}
	for i := range want {
		if roots[i] != want[i] {
			t.Fatalf("roots[%d] = %q, want %q", i, roots[i], want[i])
		}
	}
}

func TestResolvedFileRootsExpandsTilde(t *testing.T) {
	tempDir := t.TempDir()
	homeDir := filepath.Join(tempDir, "home")
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	t.Setenv("HOME", homeDir)

	cfg := &Config{
		Files: FilesConfig{
			Scope: "explicit",
			Roots: []string{"~/workspace"},
		},
	}

	roots, err := cfg.resolvedFileRoots()
	if err != nil {
		t.Fatalf("resolvedFileRoots() error = %v", err)
	}
	if len(roots) != 1 {
		t.Fatalf("root count = %d, want 1", len(roots))
	}
	if roots[0] != filepath.Join(homeDir, "workspace") {
		t.Fatalf("root = %q, want %q", roots[0], filepath.Join(homeDir, "workspace"))
	}
}

func TestBuildLoopAllowsReadingSkillFiles(t *testing.T) {
	tempDir := t.TempDir()
	skillDir := filepath.Join(tempDir, "skills", "find-skills")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	skillPath := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte("# Find Skills\nUse this skill."), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg := &Config{
		Provider: ProviderConfig{
			Kind:   "openai",
			APIKey: "dummy-key",
			Model:  "gpt-5",
		},
		Files: FilesConfig{
			Scope: "workspace",
		},
		Skills: SkillsConfig{
			Roots: []string{filepath.Join(tempDir, "skills")},
		},
		Tools: ToolsConfig{
			Enabled: []string{"read_file"},
		},
	}

	loop, err := cfg.BuildLoop(BuildOptions{})
	if err != nil {
		t.Fatalf("BuildLoop() error = %v", err)
	}

	result := loop.Tools.Execute(context.Background(), "read_file", map[string]any{
		"path": skillPath,
	})
	if result == nil || result.IsError {
		t.Fatalf("read_file result = %#v, want success", result)
	}
	if !strings.Contains(result.ForModel, "Use this skill.") {
		t.Fatalf("ForModel = %q, want skill file content", result.ForModel)
	}
}

func TestBuildLoopPassesBashApprovalPolicy(t *testing.T) {
	cfg := &Config{
		Provider: ProviderConfig{
			Kind:   "openai",
			APIKey: "dummy-key",
			Model:  "gpt-5",
		},
		Tools: ToolsConfig{
			Enabled: []string{"bash"},
			Bash: BashToolConfig{
				RequireApproval: true,
			},
		},
	}

	loop, err := cfg.BuildLoop(BuildOptions{})
	if err != nil {
		t.Fatalf("BuildLoop() error = %v", err)
	}

	result := loop.Tools.Execute(context.Background(), "bash", map[string]any{
		"command": "pwd",
	})
	if result == nil || result.Approval == nil {
		t.Fatalf("bash result = %#v, want approval request", result)
	}
	if result.Approval.Tool != "bash" {
		t.Fatalf("approval tool = %q, want bash", result.Approval.Tool)
	}
}

func TestBuildLoopPassesFileApprovalPolicy(t *testing.T) {
	root := t.TempDir()
	outsideRoot := t.TempDir()
	filePath := filepath.Join(outsideRoot, "note.txt")
	if err := os.WriteFile(filePath, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg := &Config{
		Provider: ProviderConfig{
			Kind:   "openai",
			APIKey: "dummy-key",
			Model:  "gpt-5",
		},
		Files: FilesConfig{
			Scope: "explicit",
			Roots: []string{root},
		},
		Tools: ToolsConfig{
			Enabled: []string{"read_file", "edit_file", "write_file"},
		},
	}

	loop, err := cfg.BuildLoop(BuildOptions{})
	if err != nil {
		t.Fatalf("BuildLoop() error = %v", err)
	}

	if result := loop.Tools.Execute(context.Background(), "read_file", map[string]any{"path": filePath}); result == nil || result.Approval == nil {
		t.Fatalf("read_file result = %#v, want approval", result)
	}
	if result := loop.Tools.Execute(context.Background(), "edit_file", map[string]any{"path": filePath, "old_string": "hello", "new_string": "world"}); result == nil || result.Approval == nil {
		t.Fatalf("edit_file result = %#v, want approval", result)
	}
	if result := loop.Tools.Execute(context.Background(), "write_file", map[string]any{"path": filepath.Join(outsideRoot, "new.txt"), "content": "hello"}); result == nil || result.Approval == nil {
		t.Fatalf("write_file result = %#v, want approval", result)
	}
}
