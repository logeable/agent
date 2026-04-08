package delegation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/logeable/agent/pkg/agentcore/provider"
	"github.com/logeable/agent/pkg/agentcore/tooling"
)

const DefaultMaxConcurrentChildren = 3

// Tool exposes runtime delegation as a model-callable tool.
type Tool struct {
	Runner        ChildRunner
	Batch         *BatchRunner
	MaxConcurrent int
	DefaultDepth  int
}

func (t Tool) Name() string { return "delegate_task" }

func (t Tool) Description() string {
	return strings.TrimSpace(`
Spawn one or more subagents to work on tasks in isolated contexts. Each subagent gets its own conversation and restricted toolset. Only the final summary is returned; intermediate execution does not enter your context window.

Use delegate_task for reasoning-heavy subtasks, isolated investigations, and independent workstreams that can run in parallel.
Do not use it for a single direct tool call or for purely mechanical pipelines with no reasoning value.

Subagents do not know your parent conversation history. Pass file paths, errors, constraints, and any other required context explicitly.`)
}

func (t Tool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"goal": map[string]any{
				"type":        "string",
				"description": "Single-task mode: the specific task the child should complete.",
			},
			"context": map[string]any{
				"type":        "string",
				"description": "Optional background context the child must know: file paths, errors, constraints, or project details.",
			},
			"tools": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Optional list of tool names the child is allowed to use.",
			},
			"max_iterations": map[string]any{
				"type":        "integer",
				"description": "Optional maximum tool-calling turns per child.",
			},
			"read_only": map[string]any{
				"type":        "boolean",
				"description": "Optional hint that the delegated task is read-only and safe to parallelize with other read-only tasks.",
			},
			"paths": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Optional list of paths this task will touch. Used to decide whether batch tasks can safely run in parallel.",
			},
			"tasks": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"goal": map[string]any{
							"type":        "string",
							"description": "Task-specific goal.",
						},
						"context": map[string]any{
							"type":        "string",
							"description": "Task-specific background context.",
						},
						"tools": map[string]any{
							"type":        "array",
							"items":       map[string]any{"type": "string"},
							"description": "Optional task-specific allowed tool names.",
						},
						"read_only": map[string]any{
							"type":        "boolean",
							"description": "Optional hint that this task only reads state.",
						},
						"paths": map[string]any{
							"type":        "array",
							"items":       map[string]any{"type": "string"},
							"description": "Optional list of paths this task will touch.",
						},
					},
					"required": []string{"goal"},
				},
				"maxItems":    DefaultMaxConcurrentChildren,
				"description": "Batch mode: up to 3 delegated tasks to run together.",
			},
		},
	}
}

func (t Tool) Execute(ctx context.Context, args map[string]any) *tooling.Result {
	if t.Runner == nil {
		return tooling.Error("delegate_task is not configured")
	}
	maxConcurrent := t.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = DefaultMaxConcurrentChildren
	}

	specs, err := normalizeChildSpecs(args, maxConcurrent, t.DefaultDepth)
	if err != nil {
		return tooling.Error(err.Error())
	}
	if len(specs) == 0 {
		return tooling.Error("delegate_task requires either goal or tasks")
	}

	var results []ChildResult
	if len(specs) == 1 {
		result, runErr := t.Runner.Run(ctx, specs[0])
		results = []ChildResult{result}
		if runErr != nil {
			return marshalResults(results, true)
		}
		return marshalResults(results, false)
	}

	batch := t.Batch
	if batch == nil {
		batch = &BatchRunner{Runner: t.Runner}
	}
	batchResults, runErr := batch.Run(ctx, specs)
	results = batchResults
	if runErr != nil {
		return marshalResults(results, true)
	}
	return marshalResults(results, false)
}

func marshalResults(results []ChildResult, isError bool) *tooling.Result {
	payload := make([]map[string]any, 0, len(results))
	for idx, result := range results {
		entry := map[string]any{
			"task_index":   idx,
			"status":       childStatus(result.Err),
			"summary":      result.Summary,
			"output_files": append([]string(nil), result.OutputFiles...),
			"iterations":   result.Iterations,
			"error":        errorString(result.Err),
		}
		payload = append(payload, entry)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return tooling.Error(fmt.Sprintf("delegate_task could not encode results: %v", err))
	}
	return &tooling.Result{
		ForModel: string(data),
		ForUser:  fmt.Sprintf("Delegated %d task(s)", len(results)),
		IsError:  isError,
		Metadata: map[string]any{
			"delegated_tasks": len(results),
		},
	}
}

func normalizeChildSpecs(args map[string]any, maxConcurrent int, defaultDepth int) ([]ChildSpec, error) {
	if maxConcurrent <= 0 {
		maxConcurrent = DefaultMaxConcurrentChildren
	}

	if rawTasks, ok := args["tasks"]; ok && rawTasks != nil {
		items, ok := rawTasks.([]any)
		if !ok {
			return nil, fmt.Errorf("delegate_task tasks must be an array")
		}
		specs := make([]ChildSpec, 0, min(maxConcurrent, len(items)))
		for idx, item := range items {
			if len(specs) >= maxConcurrent {
				break
			}
			taskMap, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("delegate_task task %d must be an object", idx)
			}
			spec, err := buildChildSpec(taskMap, defaultDepth)
			if err != nil {
				return nil, fmt.Errorf("delegate_task task %d: %w", idx, err)
			}
			spec.TaskIndex = idx
			specs = append(specs, spec)
		}
		return specs, nil
	}

	spec, err := buildChildSpec(args, defaultDepth)
	if err != nil {
		return nil, err
	}
	return []ChildSpec{spec}, nil
}

func buildChildSpec(args map[string]any, defaultDepth int) (ChildSpec, error) {
	goal, _ := args["goal"].(string)
	if strings.TrimSpace(goal) == "" {
		return ChildSpec{}, fmt.Errorf("goal is required")
	}
	spec := ChildSpec{
		Goal:           strings.TrimSpace(goal),
		ContextSummary: strings.TrimSpace(stringArg(args, "context")),
		Tools:          stringSliceArg(args, "tools"),
		ReadOnly:       boolArg(args, "read_only"),
		Paths:          stringSliceArg(args, "paths"),
		Depth:          defaultDepth,
	}
	if maxIterations, ok := intArg(args, "max_iterations"); ok {
		spec.MaxIterations = maxIterations
	}
	return spec, nil
}

// TruncateDelegationCalls caps total delegated children within one tool-call batch.
func TruncateDelegationCalls(calls []provider.ToolCall, maxChildren int) []provider.ToolCall {
	if maxChildren <= 0 {
		maxChildren = DefaultMaxConcurrentChildren
	}
	remaining := maxChildren
	out := make([]provider.ToolCall, 0, len(calls))
	for _, call := range calls {
		if call.Name != "delegate_task" {
			out = append(out, call)
			continue
		}
		if remaining <= 0 {
			continue
		}

		childCount := requestedChildCount(call.Arguments)
		if childCount <= remaining {
			out = append(out, call)
			remaining -= childCount
			continue
		}

		trimmed := call
		if rawTasks, ok := call.Arguments["tasks"].([]any); ok && len(rawTasks) > remaining {
			cloneArgs := cloneArgs(call.Arguments)
			cloneArgs["tasks"] = rawTasks[:remaining]
			trimmed.Arguments = cloneArgs
			out = append(out, trimmed)
			remaining = 0
			continue
		}
		if childCount > 0 {
			out = append(out, call)
			remaining--
		}
	}
	return out
}

func requestedChildCount(args map[string]any) int {
	if rawTasks, ok := args["tasks"].([]any); ok && len(rawTasks) > 0 {
		return len(rawTasks)
	}
	if goal := stringArg(args, "goal"); strings.TrimSpace(goal) != "" {
		return 1
	}
	return 0
}

func cloneArgs(args map[string]any) map[string]any {
	out := make(map[string]any, len(args))
	for key, value := range args {
		out[key] = value
	}
	return out
}

func stringArg(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return value
}

func stringSliceArg(args map[string]any, key string) []string {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil
	}
	switch items := raw.(type) {
	case []string:
		out := make([]string, 0, len(items))
		for _, item := range items {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(items))
		for _, item := range items {
			text, _ := item.(string)
			if trimmed := strings.TrimSpace(text); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	default:
		return nil
	}
}

func intArg(args map[string]any, key string) (int, bool) {
	switch value := args[key].(type) {
	case int:
		return value, true
	case int64:
		return int(value), true
	case float64:
		return int(value), true
	default:
		return 0, false
	}
}

func boolArg(args map[string]any, key string) bool {
	value, _ := args[key].(bool)
	return value
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
