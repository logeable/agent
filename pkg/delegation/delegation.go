package delegation

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/logeable/agent/pkg/agentcore/agent"
	"github.com/logeable/agent/pkg/agentcore/provider"
	"github.com/logeable/agent/pkg/agentcore/session"
	"github.com/logeable/agent/pkg/agentcore/tooling"
	"github.com/logeable/agent/pkg/orchestration"
)

// ChildSpec describes one delegated task.
type ChildSpec struct {
	TaskIndex      int
	Goal           string
	ContextSummary string
	WorkDir        string
	SessionKey     string
	Tools          []string
	Model          string
	MaxIterations  int
	Depth          int
	ParallelGroup  string
	ReadOnly       bool
	Paths          []string
}

// ChildResult contains the parent-visible output of a delegated child.
type ChildResult struct {
	Summary     string
	OutputFiles []string
	Iterations  int
	Err         error
}

// ChildRunner executes one delegated child task.
type ChildRunner interface {
	Run(ctx context.Context, spec ChildSpec) (ChildResult, error)
}

// DelegationPolicy controls child validation, parallelism, and result shaping.
type DelegationPolicy interface {
	Validate(spec ChildSpec) error
	CanRunParallel(a, b ChildSpec) bool
	FilterSummary(result ChildResult) ChildResult
}

// Budget tracks a bounded execution budget.
type Budget struct {
	maxTotal int64
	used     int64
}

func NewBudget(maxTotal int) Budget {
	if maxTotal < 0 {
		maxTotal = 0
	}
	return Budget{maxTotal: int64(maxTotal)}
}

func (b *Budget) Consume() bool {
	if b == nil || b.maxTotal == 0 {
		return true
	}
	for {
		used := atomic.LoadInt64(&b.used)
		if used >= b.maxTotal {
			return false
		}
		if atomic.CompareAndSwapInt64(&b.used, used, used+1) {
			return true
		}
	}
}

func (b *Budget) Refund() {
	if b == nil || b.maxTotal == 0 {
		return
	}
	for {
		used := atomic.LoadInt64(&b.used)
		if used <= 0 {
			return
		}
		if atomic.CompareAndSwapInt64(&b.used, used, used-1) {
			return
		}
	}
}

func (b *Budget) Remaining() int {
	if b == nil || b.maxTotal == 0 {
		return 0
	}
	used := atomic.LoadInt64(&b.used)
	remaining := b.maxTotal - used
	if remaining < 0 {
		return 0
	}
	return int(remaining)
}

// LoopFactory creates an isolated child loop for one child spec.
type LoopFactory func(ctx context.Context, spec ChildSpec) (*agent.Loop, error)

type ResultFilter func(result *tooling.Result)

// LoopChildRunner executes delegated children on top of agent.Loop.
type LoopChildRunner struct {
	Factory  LoopFactory
	Policy   DelegationPolicy
	Events   *orchestration.EventBus
	Approval agent.ApprovalHandler
}

func (r LoopChildRunner) Run(ctx context.Context, spec ChildSpec) (ChildResult, error) {
	if r.Factory == nil {
		return ChildResult{}, fmt.Errorf("child runner factory is nil")
	}
	if r.Policy == nil {
		r.Policy = DefaultPolicy{}
	}
	if err := r.Policy.Validate(spec); err != nil {
		return ChildResult{}, err
	}

	if r.Events != nil {
		r.Events.Emit(orchestration.Event{
			Kind: orchestration.EventChildStarted,
			Payload: map[string]any{
				"task_index":  spec.TaskIndex,
				"session_key": spec.SessionKey,
				"depth":       spec.Depth,
				"parallel":    spec.ParallelGroup,
			},
		})
	}

	loop, err := r.Factory(ctx, spec)
	if err != nil {
		return ChildResult{}, err
	}
	if loop == nil {
		return ChildResult{}, fmt.Errorf("child runner factory returned nil loop")
	}
	defer loop.Close()

	if loop.Events == nil {
		loop.Events = agent.NewEventBus()
		defer loop.Events.Close()
	}
	loop.Approval = r.Approval

	var mu sync.Mutex
	var outputFiles []string
	loop.Tools = cloneRegistry(loop.Tools, spec.Tools, func(result *tooling.Result) {
		if result == nil {
			return
		}
		path, _ := result.Metadata["path"].(string)
		if strings.TrimSpace(path) == "" {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if !containsString(outputFiles, path) {
			outputFiles = append(outputFiles, path)
		}
	})

	if loop.Sessions == nil {
		loop.Sessions = session.NewMemoryStore()
	}

	userPrompt := strings.TrimSpace(spec.Goal)
	if strings.TrimSpace(spec.ContextSummary) != "" {
		userPrompt = strings.TrimSpace(spec.ContextSummary) + "\n\n" + userPrompt
	}
	sessionKey := strings.TrimSpace(spec.SessionKey)
	if sessionKey == "" {
		sessionKey = fmt.Sprintf("child:%d", time.Now().UnixNano())
	}

	summary, err := loop.Process(ctx, sessionKey, userPrompt)
	result := ChildResult{
		Summary:     summary,
		OutputFiles: append([]string(nil), outputFiles...),
		Iterations:  len(loop.Sessions.GetHistory(sessionKey)),
		Err:         err,
	}
	result = r.Policy.FilterSummary(result)
	if r.Events != nil {
		r.Events.Emit(orchestration.Event{
			Kind: orchestration.EventChildFinished,
			Payload: map[string]any{
				"task_index":   spec.TaskIndex,
				"session_key":  sessionKey,
				"depth":        spec.Depth,
				"status":       childStatus(err),
				"output_files": append([]string(nil), result.OutputFiles...),
				"error":        errorString(err),
			},
		})
	}
	return result, err
}

// BatchRunner runs multiple delegated children with policy-controlled fan-out.
type BatchRunner struct {
	Runner ChildRunner
	Policy DelegationPolicy
}

func (r BatchRunner) Run(ctx context.Context, specs []ChildSpec) ([]ChildResult, error) {
	if r.Runner == nil {
		return nil, fmt.Errorf("batch runner child runner is nil")
	}
	if r.Policy == nil {
		r.Policy = DefaultPolicy{}
	}
	results := make([]ChildResult, len(specs))
	for i := 0; i < len(specs); {
		if i == len(specs)-1 {
			result, err := r.Runner.Run(ctx, specs[i])
			results[i] = result
			if err != nil {
				return results, err
			}
			i++
			continue
		}
		if !r.Policy.CanRunParallel(specs[i], specs[i+1]) {
			result, err := r.Runner.Run(ctx, specs[i])
			results[i] = result
			if err != nil {
				return results, err
			}
			i++
			continue
		}

		var wg sync.WaitGroup
		errCh := make(chan error, 2)
		for idx := i; idx < i+2; idx++ {
			wg.Add(1)
			go func(pos int) {
				defer wg.Done()
				result, err := r.Runner.Run(ctx, specs[pos])
				results[pos] = result
				if err != nil {
					errCh <- err
				}
			}(idx)
		}
		wg.Wait()
		close(errCh)
		for err := range errCh {
			if err != nil {
				return results, err
			}
		}
		i += 2
	}
	return results, nil
}

// DefaultPolicy is the default in-process delegation policy.
type DefaultPolicy struct {
	MaxDepth      int
	MaxConcurrent int
	BlockedTools  []string
}

func (p DefaultPolicy) Validate(spec ChildSpec) error {
	if strings.TrimSpace(spec.Goal) == "" {
		return fmt.Errorf("delegated child goal is required")
	}
	maxDepth := p.MaxDepth
	if maxDepth <= 0 {
		maxDepth = 2
	}
	if spec.Depth > maxDepth {
		return fmt.Errorf("delegation depth %d exceeds max depth %d", spec.Depth, maxDepth)
	}
	blocked := p.blockedTools()
	for _, tool := range spec.Tools {
		if blocked[strings.TrimSpace(tool)] {
			return fmt.Errorf("tool %q is blocked for delegated children", tool)
		}
	}
	return nil
}

func (p DefaultPolicy) CanRunParallel(a, b ChildSpec) bool {
	if a.ReadOnly && b.ReadOnly {
		return true
	}
	if len(a.Paths) == 0 || len(b.Paths) == 0 {
		return false
	}
	for _, left := range a.Paths {
		for _, right := range b.Paths {
			if pathsOverlap(left, right) {
				return false
			}
		}
	}
	return true
}

func (p DefaultPolicy) FilterSummary(result ChildResult) ChildResult {
	result.OutputFiles = dedupeStrings(result.OutputFiles)
	return result
}

func (p DefaultPolicy) blockedTools() map[string]bool {
	tools := map[string]bool{
		"delegate_task": true,
		"delegation":    true,
		"codeexec":      true,
		"automation":    true,
	}
	for _, tool := range p.BlockedTools {
		if name := strings.TrimSpace(tool); name != "" {
			tools[name] = true
		}
	}
	return tools
}

type observedTool struct {
	inner    tooling.Tool
	onResult ResultFilter
}

func (t observedTool) Name() string               { return t.inner.Name() }
func (t observedTool) Description() string        { return t.inner.Description() }
func (t observedTool) Parameters() map[string]any { return t.inner.Parameters() }
func (t observedTool) Execute(ctx context.Context, args map[string]any) *tooling.Result {
	result := t.inner.Execute(ctx, args)
	if t.onResult != nil {
		t.onResult(result)
	}
	return result
}

func cloneRegistry(source *tooling.Registry, allowed []string, onResult ResultFilter) *tooling.Registry {
	registry := tooling.NewRegistry()
	if source == nil {
		return registry
	}
	allowAll := len(allowed) == 0
	allowedSet := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		name = strings.TrimSpace(name)
		if name != "" {
			allowedSet[name] = true
		}
	}
	for _, tool := range source.Tools() {
		if !allowAll && !allowedSet[tool.Name()] {
			continue
		}
		registry.Register(observedTool{inner: tool, onResult: onResult})
	}
	return registry
}

func containsString(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

func dedupeStrings(items []string) []string {
	out := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func pathsOverlap(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if left == "." || right == "." {
		return false
	}
	if left == right {
		return true
	}
	leftPrefix := left + string(filepath.Separator)
	rightPrefix := right + string(filepath.Separator)
	return strings.HasPrefix(left, rightPrefix) || strings.HasPrefix(right, leftPrefix)
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func childStatus(err error) string {
	if err == nil {
		return "completed"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "cancelled"
	}
	return "failed"
}

// ScriptedLoopFactory returns a loop factory useful for tests and local wiring.
func ScriptedLoopFactory(model provider.ChatModel, tools *tooling.Registry, systemPrompt string) LoopFactory {
	return func(_ context.Context, spec ChildSpec) (*agent.Loop, error) {
		if model == nil {
			return nil, errors.New("model is nil")
		}
		loop := &agent.Loop{
			Model:         model,
			Tools:         cloneRegistry(tools, spec.Tools, nil),
			Sessions:      session.NewMemoryStore(),
			Context:       agent.ContextBuilder{SystemPrompt: systemPrompt},
			MaxIterations: spec.MaxIterations,
			Events:        agent.NewEventBus(),
		}
		return loop, nil
	}
}
