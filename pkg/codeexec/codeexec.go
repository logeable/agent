package codeexec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/logeable/agent/pkg/agentcore/tooling"
	"github.com/logeable/agent/pkg/orchestration"
)

var blockedTools = map[string]bool{
	"delegation":    true,
	"delegate_task": true,
	"automation":    true,
	"codeexec":      true,
}

type ExecutionRequest struct {
	Script       string
	WorkDir      string
	AllowedTools []string
	Timeout      time.Duration
	MaxToolCalls int
}

type ExecutionResult struct {
	Stdout    string
	Stderr    string
	ToolCalls int
	Err       error
}

type Sandbox interface {
	Execute(ctx context.Context, req ExecutionRequest) (ExecutionResult, error)
}

type RPCBridge interface {
	Serve(ctx context.Context) error
	Dispatch(ctx context.Context, tool string, args map[string]any) (*tooling.Result, error)
}

type ExecutionPolicy interface {
	Validate(req ExecutionRequest) error
}

type DefaultExecutionPolicy struct {
	MaxTimeout     time.Duration
	MaxToolCalls   int
	MaxStdoutBytes int
	MaxStderrBytes int
}

func (p DefaultExecutionPolicy) Validate(req ExecutionRequest) error {
	if strings.TrimSpace(req.Script) == "" {
		return fmt.Errorf("codeexec script is required")
	}
	if req.Timeout < 0 {
		return fmt.Errorf("codeexec timeout must be zero or greater")
	}
	if req.MaxToolCalls < 0 {
		return fmt.Errorf("codeexec max tool calls must be zero or greater")
	}
	if p.MaxTimeout > 0 && req.Timeout > p.MaxTimeout {
		return fmt.Errorf("codeexec timeout %s exceeds max timeout %s", req.Timeout, p.MaxTimeout)
	}
	if p.MaxToolCalls > 0 && req.MaxToolCalls > p.MaxToolCalls {
		return fmt.Errorf("codeexec max tool calls %d exceeds policy limit %d", req.MaxToolCalls, p.MaxToolCalls)
	}
	for _, tool := range req.AllowedTools {
		if blockedTools[strings.TrimSpace(tool)] {
			return fmt.Errorf("tool %q is blocked inside codeexec", tool)
		}
	}
	return nil
}

// FileRPCBridge implements a polling file-based RPC transport.
type FileRPCBridge struct {
	Registry     *tooling.Registry
	AllowedTools map[string]bool
	Dir          string
	MaxToolCalls int

	mu        sync.Mutex
	toolCalls int
	seen      map[string]struct{}
}

func (b *FileRPCBridge) Serve(ctx context.Context) error {
	if b.Registry == nil {
		return fmt.Errorf("rpc bridge registry is nil")
	}
	if strings.TrimSpace(b.Dir) == "" {
		return fmt.Errorf("rpc bridge dir is required")
	}
	if err := os.MkdirAll(b.Dir, 0o755); err != nil {
		return err
	}
	if b.seen == nil {
		b.seen = make(map[string]struct{})
	}

	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			entries, err := os.ReadDir(b.Dir)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				name := entry.Name()
				if entry.IsDir() || !strings.HasPrefix(name, "req_") || filepath.Ext(name) != ".json" {
					continue
				}
				if _, ok := b.seen[name]; ok {
					continue
				}
				b.seen[name] = struct{}{}
				if err := b.handleRequest(ctx, filepath.Join(b.Dir, name)); err != nil {
					return err
				}
			}
		}
	}
}

func (b *FileRPCBridge) handleRequest(ctx context.Context, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var payload struct {
		ID   string         `json:"id"`
		Tool string         `json:"tool"`
		Args map[string]any `json:"args"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}

	result, dispatchErr := b.Dispatch(ctx, payload.Tool, payload.Args)
	response := map[string]any{
		"ok":       dispatchErr == nil && result != nil && !result.IsError,
		"tool":     payload.Tool,
		"error":    errorString(dispatchErr),
		"forModel": "",
		"metadata": map[string]any{},
	}
	if result != nil {
		response["forModel"] = result.ForModel
		response["metadata"] = result.Metadata
		if dispatchErr == nil && result.Err != nil {
			response["error"] = result.Err.Error()
		}
	}
	respPath := filepath.Join(filepath.Dir(path), strings.Replace(filepath.Base(path), "req_", "resp_", 1))
	body, err := json.Marshal(response)
	if err != nil {
		return err
	}
	if err := os.WriteFile(respPath, body, 0o644); err != nil {
		return err
	}
	return os.Remove(path)
}

func (b *FileRPCBridge) Dispatch(ctx context.Context, tool string, args map[string]any) (*tooling.Result, error) {
	if b.Registry == nil {
		return nil, fmt.Errorf("rpc bridge registry is nil")
	}
	if !b.allowed(tool) {
		return nil, fmt.Errorf("tool %q is not allowed inside codeexec", tool)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.MaxToolCalls > 0 && b.toolCalls >= b.MaxToolCalls {
		return nil, fmt.Errorf("codeexec exceeded max tool calls %d", b.MaxToolCalls)
	}
	b.toolCalls++
	return b.Registry.Execute(ctx, tool, args), nil
}

func (b *FileRPCBridge) allowed(name string) bool {
	if blockedTools[strings.TrimSpace(name)] {
		return false
	}
	if len(b.AllowedTools) == 0 {
		return true
	}
	return b.AllowedTools[name]
}

func (b *FileRPCBridge) ToolCalls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.toolCalls
}

// LocalSandbox executes Python scripts with file-based RPC back to a registry.
type LocalSandbox struct {
	Registry *tooling.Registry
	Policy   ExecutionPolicy
	Events   *orchestration.EventBus
}

func (s LocalSandbox) Execute(ctx context.Context, req ExecutionRequest) (ExecutionResult, error) {
	if s.Registry == nil {
		return ExecutionResult{}, fmt.Errorf("codeexec registry is nil")
	}
	policy := s.Policy
	if policy == nil {
		policy = DefaultExecutionPolicy{
			MaxTimeout:     5 * time.Minute,
			MaxToolCalls:   50,
			MaxStdoutBytes: 64 * 1024,
			MaxStderrBytes: 16 * 1024,
		}
	}
	if err := policy.Validate(req); err != nil {
		return ExecutionResult{}, err
	}

	if s.Events != nil {
		s.Events.Emit(orchestration.Event{
			Kind: orchestration.EventCodeExecStarted,
			Payload: map[string]any{
				"workdir": req.WorkDir,
			},
		})
	}

	tmpDir, err := os.MkdirTemp("", "agent-codeexec-*")
	if err != nil {
		return ExecutionResult{}, err
	}
	defer os.RemoveAll(tmpDir)

	rpcDir := filepath.Join(tmpDir, "rpc")
	if err := os.MkdirAll(rpcDir, 0o755); err != nil {
		return ExecutionResult{}, err
	}
	scriptPath := filepath.Join(tmpDir, "main.py")
	if err := os.WriteFile(scriptPath, []byte(req.Script), 0o644); err != nil {
		return ExecutionResult{}, err
	}
	modulePath := filepath.Join(tmpDir, "agent_tools.py")
	if err := os.WriteFile(modulePath, []byte(helperModuleSource()), 0o644); err != nil {
		return ExecutionResult{}, err
	}

	maxToolCalls := req.MaxToolCalls
	if maxToolCalls <= 0 {
		maxToolCalls = 50
	}
	bridge := &FileRPCBridge{
		Registry:     s.Registry,
		AllowedTools: allowedToolSet(req.AllowedTools),
		Dir:          rpcDir,
		MaxToolCalls: maxToolCalls,
	}

	bridgeCtx, cancelBridge := context.WithCancel(ctx)
	defer cancelBridge()
	bridgeErrCh := make(chan error, 1)
	go func() {
		err := bridge.Serve(bridgeCtx)
		if err != nil && err != context.Canceled {
			bridgeErrCh <- err
			return
		}
		bridgeErrCh <- nil
	}()

	runCtx := ctx
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(runCtx, "python3", scriptPath)
	cmd.Env = append(os.Environ(),
		"PYTHONPATH="+tmpDir,
		"AGENT_RPC_DIR="+rpcDir,
	)
	if strings.TrimSpace(req.WorkDir) != "" {
		cmd.Dir = req.WorkDir
	} else {
		cmd.Dir = tmpDir
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	stdoutLimit := 64 * 1024
	stderrLimit := 16 * 1024
	if typed, ok := policy.(DefaultExecutionPolicy); ok {
		if typed.MaxStdoutBytes > 0 {
			stdoutLimit = typed.MaxStdoutBytes
		}
		if typed.MaxStderrBytes > 0 {
			stderrLimit = typed.MaxStderrBytes
		}
	}
	cmd.Stdout = &limitedBuffer{buf: &stdout, limit: stdoutLimit}
	cmd.Stderr = &limitedBuffer{buf: &stderr, limit: stderrLimit}

	runErr := cmd.Run()
	cancelBridge()
	bridgeErr := <-bridgeErrCh
	result := ExecutionResult{
		Stdout:    stdout.String(),
		Stderr:    stderr.String(),
		ToolCalls: bridge.ToolCalls(),
		Err:       runErr,
	}
	if bridgeErr != nil && result.Err == nil {
		result.Err = bridgeErr
	}
	if runCtx.Err() != nil && result.Err == nil {
		result.Err = runCtx.Err()
	}

	if s.Events != nil {
		s.Events.Emit(orchestration.Event{
			Kind: orchestration.EventCodeExecFinished,
			Payload: map[string]any{
				"tool_calls": result.ToolCalls,
				"error":      errorString(result.Err),
			},
		})
	}
	return result, result.Err
}

func allowedToolSet(names []string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name != "" {
			out[name] = true
		}
	}
	return out
}

func helperModuleSource() string {
	return strings.TrimSpace(`
import json
import os
import time
import uuid

RPC_DIR = os.environ["AGENT_RPC_DIR"]

def call_tool(name, **kwargs):
    req_id = str(uuid.uuid4())
    req_path = os.path.join(RPC_DIR, f"req_{req_id}.json")
    resp_path = os.path.join(RPC_DIR, f"resp_{req_id}.json")
    with open(req_path, "w", encoding="utf-8") as f:
        json.dump({"id": req_id, "tool": name, "args": kwargs}, f)
    while not os.path.exists(resp_path):
        time.sleep(0.01)
    with open(resp_path, "r", encoding="utf-8") as f:
        payload = json.load(f)
    os.remove(resp_path)
    if payload.get("error"):
        raise RuntimeError(payload["error"])
    return payload.get("forModel", "")
`) + "\n"
}

func AllowedToolNames(registry *tooling.Registry) []string {
	if registry == nil {
		return nil
	}
	names := make([]string, 0, len(registry.Tools()))
	for _, tool := range registry.Tools() {
		if blockedTools[tool.Name()] {
			continue
		}
		names = append(names, tool.Name())
	}
	sort.Strings(names)
	return names
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type limitedBuffer struct {
	buf   *bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.buf == nil {
		return len(p), nil
	}
	if b.limit <= 0 {
		return b.buf.Write(p)
	}
	remaining := b.limit - b.buf.Len()
	if remaining <= 0 {
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = b.buf.Write(p[:remaining])
		return len(p), nil
	}
	return b.buf.Write(p)
}
