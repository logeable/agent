package agentapp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/logeable/agent/pkg/delegation"
)

func TestBuildRuntimeWiresOrchestrationModules(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "agent.toml")
	data := []byte(`
name = "test"

[provider]
kind = "ollama"
model = "tiny"

[tools]
enabled = ["read_file", "delegate_task"]

[orchestration.delegation]
enabled = true
max_depth = 2

[orchestration.automation]
default_interval_ms = 1000

[orchestration.codeexec]
timeout_ms = 1000
`)
	if err := os.WriteFile(profilePath, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	runtime, err := BuildRuntime(LoopOptions{
		ProfileName: profilePath,
		WorkDir:     dir,
	})
	if err != nil {
		t.Fatalf("BuildRuntime() error = %v", err)
	}
	defer runtime.Close()

	if runtime.Loop == nil || runtime.Events == nil || runtime.Delegation == nil || runtime.Automation == nil || runtime.CodeExec == nil {
		t.Fatalf("runtime = %+v, want all modules wired", runtime)
	}
	if runtime.CodeExecPrompt == "" {
		t.Fatal("CodeExecPrompt = empty, want prompt guidance")
	}
	if runtime.Automation == nil {
		t.Fatal("Automation = nil")
	}
	if _, ok := runtime.Loop.Tools.Get("delegate_task"); !ok {
		t.Fatal("main runtime loop missing delegate_task tool")
	}
	childLoop, err := runtime.Delegation.Factory(context.Background(), delegation.ChildSpec{Goal: "inspect", Depth: 1})
	if err != nil {
		t.Fatalf("child factory error = %v", err)
	}
	defer childLoop.Close()
	if _, ok := childLoop.Tools.Get("delegate_task"); ok {
		t.Fatal("child loop unexpectedly includes delegate_task")
	}
}
