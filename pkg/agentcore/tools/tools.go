package tools

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/logeable/agent/pkg/agentcore/provider"
)

// Tool is the runtime-side interface implemented by concrete capabilities.
//
// What:
// A tool has a stable name, a human-readable description, a parameter schema,
// and one Execute method.
//
// Why:
// The model needs a predictable contract for "what can I call?", while the
// runtime needs a predictable contract for "how do I execute it?".
type Tool interface {
	Name() string
	Description() string
	Parameters() map[string]any
	Execute(ctx context.Context, args map[string]any) *Result
}

// Result captures the outcome of a tool execution.
//
// What:
// We separate the text intended for the model (`ForModel`) from the text
// intended for the end user (`ForUser`).
//
// Why:
// In real agents these are often different:
// - the model may need structured or verbose feedback
// - the user may need a short, friendly summary
// Keeping both fields now makes later expansion easier.
type Result struct {
	ForModel string
	ForUser  string
	IsError  bool
	Err      error
}

// ContentForModel returns the safest fallback payload to place into a tool message.
//
// Why:
// The agent loop always needs some content to feed back into the model after a
// tool call. This helper centralizes the fallback rules so the loop stays simple.
func (r *Result) ContentForModel() string {
	if r == nil {
		return "tool returned nil result"
	}
	switch {
	case r.ForModel != "":
		return r.ForModel
	case r.Err != nil:
		return r.Err.Error()
	default:
		return ""
	}
}

// Error creates a conventional error result.
//
// Why:
// Many tools fail in the same boring way. A helper keeps those call sites short
// and makes the returned shape consistent.
func Error(msg string) *Result {
	return &Result{
		ForModel: msg,
		ForUser:  msg,
		IsError:  true,
		Err:      errors.New(msg),
	}
}

// Registry stores tools by name and offers the two operations the loop needs:
// discover tools and execute a tool by name.
//
// Why:
// The agent loop should not know about concrete tool implementations.
// It should only ask:
// - "what tools are available?"
// - "please run tool X with args Y"
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewRegistry creates an empty tool registry.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register makes a tool callable by the agent loop.
func (r *Registry) Register(tool Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[tool.Name()] = tool
}

// Get returns a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.tools[name]
	return tool, ok
}

// Execute resolves a tool by name and runs it.
//
// Why:
// This keeps name lookup and "tool not found" handling in one place instead of
// duplicating it in the loop.
func (r *Registry) Execute(ctx context.Context, name string, args map[string]any) *Result {
	tool, ok := r.Get(name)
	if !ok {
		return Error(fmt.Sprintf("tool %q not found", name))
	}
	return tool.Execute(ctx, args)
}

// Definitions converts registered runtime tools into the provider-facing schema.
//
// What:
// The agent loop sends these definitions to the model before each turn.
//
// Why:
// The runtime needs concrete Tool implementations, but the model only needs a
// description of callable capabilities. This method bridges those two views.
func (r *Registry) Definitions() []provider.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sortStrings(names)

	defs := make([]provider.ToolDefinition, 0, len(names))
	for _, name := range names {
		tool := r.tools[name]
		defs = append(defs, provider.ToolDefinition{
			Name:        tool.Name(),
			Description: tool.Description(),
			Parameters:  tool.Parameters(),
		})
	}
	return defs
}

// sortStrings is intentionally tiny and local.
//
// Why:
// We only need deterministic ordering for novice-friendly testability and
// reproducible prompts. Avoiding another import keeps this teaching module small.
func sortStrings(values []string) {
	for i := 0; i < len(values); i++ {
		for j := i + 1; j < len(values); j++ {
			if values[j] < values[i] {
				values[i], values[j] = values[j], values[i]
			}
		}
	}
}
