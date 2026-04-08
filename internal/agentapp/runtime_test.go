package agentapp

import (
	"os"
	"path/filepath"
	"testing"
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
enabled = ["read_file"]

[orchestration.delegation]
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
}
