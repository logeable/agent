package main

import (
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
