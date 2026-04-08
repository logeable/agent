package delegation

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/logeable/agent/pkg/agentcore/agent"
	"github.com/logeable/agent/pkg/agentcore/provider"
	"github.com/logeable/agent/pkg/agentcore/session"
	"github.com/logeable/agent/pkg/agentcore/tooling"
)

type scriptedModel struct {
	responses []*provider.Response
	calls     int
}

func (m *scriptedModel) Chat(ctx context.Context, _ []provider.Message, _ []provider.ToolDefinition, _ string, _ map[string]any) (*provider.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if m.calls >= len(m.responses) {
		return nil, fmt.Errorf("unexpected extra model call")
	}
	resp := m.responses[m.calls]
	m.calls++
	return resp, nil
}

type artifactTool struct{}

func (artifactTool) Name() string        { return "artifact" }
func (artifactTool) Description() string { return "creates an artifact" }
func (artifactTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}
func (artifactTool) Execute(_ context.Context, _ map[string]any) *tooling.Result {
	return &tooling.Result{
		ForModel: "artifact:done",
		Metadata: map[string]any{"path": "out.txt"},
	}
}

type blockingTool struct{}

func (blockingTool) Name() string        { return "block" }
func (blockingTool) Description() string { return "blocks until cancelled" }
func (blockingTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}
func (blockingTool) Execute(ctx context.Context, _ map[string]any) *tooling.Result {
	<-ctx.Done()
	return &tooling.Result{ForModel: ctx.Err().Error(), IsError: true, Err: ctx.Err()}
}

func TestLoopChildRunnerReturnsSummaryAndOutputFiles(t *testing.T) {
	model := &scriptedModel{
		responses: []*provider.Response{
			{ToolCalls: []provider.ToolCall{{ID: "1", Name: "artifact", Arguments: map[string]any{}}}},
			{Content: "child summary"},
		},
	}
	registry := tooling.NewRegistry()
	registry.Register(artifactTool{})
	runner := LoopChildRunner{
		Factory: ScriptedLoopFactory(model, registry, "You are a child."),
		Policy:  DefaultPolicy{},
	}

	got, err := runner.Run(context.Background(), ChildSpec{
		Goal:       "do the work",
		SessionKey: "child-1",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got.Summary != "child summary" {
		t.Fatalf("summary = %q, want child summary", got.Summary)
	}
	if len(got.OutputFiles) != 1 || got.OutputFiles[0] != "out.txt" {
		t.Fatalf("output files = %#v, want out.txt", got.OutputFiles)
	}
}

func TestLoopChildRunnerBlocksRestrictedTools(t *testing.T) {
	runner := LoopChildRunner{
		Factory: func(context.Context, ChildSpec) (*agent.Loop, error) { return &agent.Loop{}, nil },
		Policy:  DefaultPolicy{},
	}

	_, err := runner.Run(context.Background(), ChildSpec{
		Goal:  "no recursion",
		Tools: []string{"codeexec"},
	})
	if err == nil {
		t.Fatal("Run() error = nil, want blocked tool error")
	}
}

func TestBatchRunnerParallelizesReadOnlyTasks(t *testing.T) {
	var mu sync.Mutex
	var calls int
	runner := childRunnerFunc(func(ctx context.Context, spec ChildSpec) (ChildResult, error) {
		time.Sleep(120 * time.Millisecond)
		mu.Lock()
		calls++
		mu.Unlock()
		return ChildResult{Summary: spec.Goal}, nil
	})
	batch := BatchRunner{
		Runner: runner,
		Policy: DefaultPolicy{},
	}

	start := time.Now()
	_, err := batch.Run(context.Background(), []ChildSpec{
		{Goal: "a", ReadOnly: true},
		{Goal: "b", ReadOnly: true},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed >= 220*time.Millisecond {
		t.Fatalf("elapsed = %s, want parallel execution", elapsed)
	}
}

func TestBatchRunnerSerializesOverlappingPaths(t *testing.T) {
	runner := childRunnerFunc(func(ctx context.Context, spec ChildSpec) (ChildResult, error) {
		time.Sleep(120 * time.Millisecond)
		return ChildResult{Summary: spec.Goal}, nil
	})
	batch := BatchRunner{
		Runner: runner,
		Policy: DefaultPolicy{},
	}

	start := time.Now()
	_, err := batch.Run(context.Background(), []ChildSpec{
		{Goal: "a", Paths: []string{"repo/file.txt"}},
		{Goal: "b", Paths: []string{"repo"}},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed < 220*time.Millisecond {
		t.Fatalf("elapsed = %s, want sequential execution", elapsed)
	}
}

func TestLoopChildRunnerPropagatesCancellation(t *testing.T) {
	model := &scriptedModel{
		responses: []*provider.Response{
			{ToolCalls: []provider.ToolCall{{ID: "1", Name: "block", Arguments: map[string]any{}}}},
		},
	}
	registry := tooling.NewRegistry()
	registry.Register(blockingTool{})
	runner := LoopChildRunner{
		Factory: func(_ context.Context, spec ChildSpec) (*agent.Loop, error) {
			return &agent.Loop{
				Model:         model,
				Tools:         registry,
				Sessions:      session.NewMemoryStore(),
				Context:       agent.ContextBuilder{SystemPrompt: "child"},
				MaxIterations: spec.MaxIterations,
				Events:        agent.NewEventBus(),
			}, nil
		},
		Policy: DefaultPolicy{},
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, err := runner.Run(ctx, ChildSpec{Goal: "wait"})
	if err == nil {
		t.Fatal("Run() error = nil, want cancellation")
	}
}

func TestDelegationToolSingleTask(t *testing.T) {
	tool := Tool{
		Runner: childRunnerFunc(func(ctx context.Context, spec ChildSpec) (ChildResult, error) {
			return ChildResult{Summary: "done", OutputFiles: []string{"a.txt"}, Iterations: 3}, nil
		}),
		MaxConcurrent: 3,
		DefaultDepth:  1,
	}

	result := tool.Execute(context.Background(), map[string]any{
		"goal":    "inspect",
		"context": "use path repo",
	})
	if result == nil || result.IsError {
		t.Fatalf("Execute() result = %+v, want success", result)
	}
	var payload []map[string]any
	if err := json.Unmarshal([]byte(result.ForModel), &payload); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(payload) != 1 || payload[0]["summary"] != "done" {
		t.Fatalf("payload = %#v, want one summary", payload)
	}
}

func TestDelegationToolBatchTask(t *testing.T) {
	tool := Tool{
		Runner: childRunnerFunc(func(ctx context.Context, spec ChildSpec) (ChildResult, error) {
			return ChildResult{Summary: spec.Goal}, nil
		}),
		Batch: &BatchRunner{
			Runner: childRunnerFunc(func(ctx context.Context, spec ChildSpec) (ChildResult, error) {
				return ChildResult{Summary: spec.Goal}, nil
			}),
			Policy: DefaultPolicy{},
		},
		MaxConcurrent: 3,
		DefaultDepth:  1,
	}

	result := tool.Execute(context.Background(), map[string]any{
		"tasks": []any{
			map[string]any{"goal": "a", "read_only": true},
			map[string]any{"goal": "b", "read_only": true},
		},
	})
	if result == nil || result.IsError {
		t.Fatalf("Execute() result = %+v, want success", result)
	}
	var payload []map[string]any
	if err := json.Unmarshal([]byte(result.ForModel), &payload); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(payload) != 2 {
		t.Fatalf("payload len = %d, want 2", len(payload))
	}
}

func TestTruncateDelegationCallsCapsChildrenAcrossCalls(t *testing.T) {
	calls := []provider.ToolCall{
		{
			ID:   "1",
			Name: "delegate_task",
			Arguments: map[string]any{
				"tasks": []any{
					map[string]any{"goal": "a"},
					map[string]any{"goal": "b"},
				},
			},
		},
		{
			ID:   "2",
			Name: "delegate_task",
			Arguments: map[string]any{
				"tasks": []any{
					map[string]any{"goal": "c"},
					map[string]any{"goal": "d"},
				},
			},
		},
		{
			ID:        "3",
			Name:      "read_file",
			Arguments: map[string]any{"path": "go.mod"},
		},
	}

	got := TruncateDelegationCalls(calls, 3)
	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3", len(got))
	}
	secondTasks, _ := got[1].Arguments["tasks"].([]any)
	if len(secondTasks) != 1 {
		t.Fatalf("trimmed second task count = %d, want 1", len(secondTasks))
	}
	if got[2].Name != "read_file" {
		t.Fatalf("non-delegate call was removed: %#v", got[2])
	}
}

type childRunnerFunc func(ctx context.Context, spec ChildSpec) (ChildResult, error)

func (f childRunnerFunc) Run(ctx context.Context, spec ChildSpec) (ChildResult, error) {
	return f(ctx, spec)
}
