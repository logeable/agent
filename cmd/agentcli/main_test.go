package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveProfileArgumentWithHomePrefersNamedProfile(t *testing.T) {
	homeDir := t.TempDir()
	profileDir := filepath.Join(homeDir, ".agentcli")
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		t.Fatalf("mkdir profile dir: %v", err)
	}

	namedProfile := filepath.Join(profileDir, "default.toml")
	if err := os.WriteFile(namedProfile, []byte("name = \"default\"\n"), 0o644); err != nil {
		t.Fatalf("write named profile: %v", err)
	}

	got := resolveProfileArgumentWithHome("default", homeDir)
	if got != namedProfile {
		t.Fatalf("expected named profile %q, got %q", namedProfile, got)
	}
}

func TestResolveProfileArgumentWithHomeFallsBackToPath(t *testing.T) {
	homeDir := t.TempDir()
	path := "./configs/dev.toml"

	got := resolveProfileArgumentWithHome(path, homeDir)
	if got != path {
		t.Fatalf("expected path fallback %q, got %q", path, got)
	}
}

func TestResolveProfileArgumentWithHomeAcceptsTomlName(t *testing.T) {
	homeDir := t.TempDir()
	profileDir := filepath.Join(homeDir, ".agentcli")
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		t.Fatalf("mkdir profile dir: %v", err)
	}

	namedProfile := filepath.Join(profileDir, "work.toml")
	if err := os.WriteFile(namedProfile, []byte("name = \"work\"\n"), 0o644); err != nil {
		t.Fatalf("write named profile: %v", err)
	}

	got := resolveProfileArgumentWithHome("work.toml", homeDir)
	if got != namedProfile {
		t.Fatalf("expected named profile %q, got %q", namedProfile, got)
	}
}

func TestBuildDelegationHintMessage(t *testing.T) {
	got := buildDelegationHintMessage("")
	if got == "" {
		t.Fatal("buildDelegationHintMessage() = empty")
	}
	if want := "Delegation hint"; got[:len(want)] != want {
		t.Fatalf("hint prompt prefix = %q, want %q", got[:len(want)], want)
	}
}

func TestBuildDelegationHintMessageAppendsBase(t *testing.T) {
	got := buildDelegationHintMessage("Base instruction.")
	if got == "" || got == "Base instruction." {
		t.Fatalf("buildDelegationHintMessage() = %q, want appended hint prompt", got)
	}
}

func TestBuildDelegationRequiredPrompt(t *testing.T) {
	got := buildDelegationRequiredPrompt()
	if got == "" {
		t.Fatal("buildDelegationRequiredPrompt() = empty")
	}
	if want := "This run must use delegate_task"; !strings.Contains(got, want) {
		t.Fatalf("required prompt = %q, want substring %q", got, want)
	}
}

func TestAppendTemporarySystemPrompt(t *testing.T) {
	got := appendTemporarySystemPrompt("base", "extra")
	if got != "base\n\n---\n\nextra" {
		t.Fatalf("appendTemporarySystemPrompt() = %q", got)
	}
}

func TestNormalizeLegacyLongFlags(t *testing.T) {
	got := normalizeLegacyLongFlags([]string{"models", "-profile", "agent.toml", "-m", "hi", "--stream", "-base-url=http://x"})
	want := []string{"models", "--profile", "agent.toml", "-m", "hi", "--stream", "--base-url=http://x"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("normalizeLegacyLongFlags() = %#v, want %#v", got, want)
	}
}

func TestModelsCommand(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("path = %q, want /models", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5-mini"},{"id":"gpt-5"}]}`))
	}))
	defer server.Close()

	profilePath := filepath.Join(t.TempDir(), "agent.toml")
	if err := os.WriteFile(profilePath, []byte(`
name = "test"

[provider]
kind = "openai"
`), 0o644); err != nil {
		t.Fatalf("write profile: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := newRootCommand(nil, &stdout, &stderr)
	cmd.SetArgs(normalizeLegacyLongFlags([]string{
		"models",
		"-profile", profilePath,
		"-base-url", server.URL,
		"-api-key", "test-key",
	}))
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	got := stdout.String()
	if !strings.Contains(got, "provider: openai") {
		t.Fatalf("stdout missing provider line: %q", got)
	}
	if !strings.Contains(got, "- gpt-5\n- gpt-5-mini\n") {
		t.Fatalf("stdout missing sorted models: %q", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}
