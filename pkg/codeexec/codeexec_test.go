package codeexec

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/logeable/agent/pkg/agentcore/tooling"
)

type echoTool struct{}

func (echoTool) Name() string        { return "echo" }
func (echoTool) Description() string { return "echo" }
func (echoTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}
func (echoTool) Execute(_ context.Context, args map[string]any) *tooling.Result {
	return &tooling.Result{ForModel: "echo:" + args["text"].(string)}
}

func requirePython3(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
}

func TestLocalSandboxExecutesScriptAndCallsTools(t *testing.T) {
	requirePython3(t)
	registry := tooling.NewRegistry()
	registry.Register(echoTool{})
	sandbox := LocalSandbox{Registry: registry}

	result, err := sandbox.Execute(context.Background(), ExecutionRequest{
		Script: `
from agent_tools import call_tool
print(call_tool("echo", text="hi"))
`,
		AllowedTools: []string{"echo"},
		Timeout:      2 * time.Second,
		MaxToolCalls: 2,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if strings.TrimSpace(result.Stdout) != "echo:hi" {
		t.Fatalf("stdout = %q, want echo:hi", result.Stdout)
	}
	if result.ToolCalls != 1 {
		t.Fatalf("tool calls = %d, want 1", result.ToolCalls)
	}
}

func TestLocalSandboxRejectsBlockedTools(t *testing.T) {
	registry := tooling.NewRegistry()
	registry.Register(echoTool{})
	sandbox := LocalSandbox{Registry: registry}

	_, err := sandbox.Execute(context.Background(), ExecutionRequest{
		Script:       `print("hi")`,
		AllowedTools: []string{"codeexec"},
	})
	if err == nil {
		t.Fatal("Execute() error = nil, want blocked tool error")
	}
}

func TestLocalSandboxEnforcesTimeout(t *testing.T) {
	requirePython3(t)
	registry := tooling.NewRegistry()
	registry.Register(echoTool{})
	sandbox := LocalSandbox{Registry: registry}

	_, err := sandbox.Execute(context.Background(), ExecutionRequest{
		Script:  "import time\ntime.sleep(1)\n",
		Timeout: 50 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("Execute() error = nil, want timeout")
	}
}

func TestLocalSandboxEnforcesToolCallLimit(t *testing.T) {
	requirePython3(t)
	registry := tooling.NewRegistry()
	registry.Register(echoTool{})
	sandbox := LocalSandbox{Registry: registry}

	result, err := sandbox.Execute(context.Background(), ExecutionRequest{
		Script: `
from agent_tools import call_tool
for _ in range(3):
    print(call_tool("echo", text="x"))
`,
		AllowedTools: []string{"echo"},
		Timeout:      2 * time.Second,
		MaxToolCalls: 2,
	})
	if err == nil {
		t.Fatal("Execute() error = nil, want tool-call limit failure")
	}
	if result.ToolCalls != 2 {
		t.Fatalf("tool calls = %d, want 2", result.ToolCalls)
	}
}
