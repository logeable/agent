package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/logeable/agent/pkg/agentcore/provider"
	"github.com/logeable/agent/pkg/agentcore/session"
	"github.com/logeable/agent/pkg/agentcore/tooling"
	builtintools "github.com/logeable/agent/pkg/tools"
)

type scriptedModel struct {
	responses []*provider.Response
	calls     int
}

func (m *scriptedModel) Chat(
	_ context.Context,
	_ []provider.Message,
	_ []provider.ToolDefinition,
	_ string,
	_ map[string]any,
) (*provider.Response, error) {
	if m.calls >= len(m.responses) {
		return nil, fmt.Errorf("unexpected extra model call")
	}
	resp := m.responses[m.calls]
	m.calls++
	return resp, nil
}

type streamingScriptedModel struct {
	response provider.Response
	chunks   []string
	kinds    []provider.StreamChunkKind
}

func (m *streamingScriptedModel) Chat(
	_ context.Context,
	_ []provider.Message,
	_ []provider.ToolDefinition,
	_ string,
	_ map[string]any,
) (*provider.Response, error) {
	return &m.response, nil
}

func (m *streamingScriptedModel) ChatStream(
	_ context.Context,
	_ []provider.Message,
	_ []provider.ToolDefinition,
	_ string,
	_ map[string]any,
	onChunk func(provider.StreamChunk),
) (*provider.Response, error) {
	var accumulated string
	for _, chunk := range m.chunks {
		accumulated += chunk
		kind := provider.StreamChunkKindOutputText
		if len(m.kinds) > 0 {
			kind = m.kinds[0]
			m.kinds = m.kinds[1:]
		}
		onChunk(provider.StreamChunk{
			Kind:        kind,
			Delta:       chunk,
			Accumulated: accumulated,
		})
	}
	return &m.response, nil
}

type echoTool struct{}

func (echoTool) Name() string { return "echo" }

func (echoTool) Description() string { return "Echoes the provided text" }

func (echoTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"text": map[string]any{"type": "string"},
		},
	}
}

func (echoTool) Execute(_ context.Context, args map[string]any) *tooling.Result {
	return &tooling.Result{ForModel: fmt.Sprintf("echo:%v", args["text"])}
}

type approvalTool struct{}

func (approvalTool) Name() string { return "dangerous" }

func (approvalTool) Description() string { return "Needs approval before execution" }

func (approvalTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
	}
}

func (approvalTool) Execute(ctx context.Context, _ map[string]any) *tooling.Result {
	if tooling.ToolApproved(ctx, "dangerous") {
		return &tooling.Result{ForModel: "dangerous:ok"}
	}
	return tooling.RequiresApproval(tooling.ApprovalRequest{
		ID:          "approval-1",
		Tool:        "dangerous",
		Reason:      "this action needs explicit approval",
		ActionLabel: "run dangerous action",
	})
}

func TestLoopDirectAnswer(t *testing.T) {
	model := &scriptedModel{
		responses: []*provider.Response{{Content: "hello"}},
	}
	store := session.NewMemoryStore()
	loop := Loop{
		Model:    model,
		Sessions: store,
		Context:  ContextBuilder{SystemPrompt: "You are an agent."},
	}

	got, err := loop.Process(context.Background(), "s1", "hi")
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if got != "hello" {
		t.Fatalf("Process() = %q, want %q", got, "hello")
	}
}

func TestLoopToolRoundTrip(t *testing.T) {
	model := &scriptedModel{
		responses: []*provider.Response{
			{
				Content: "Let me use a tool.",
				ToolCalls: []provider.ToolCall{
					{ID: "call-1", Name: "echo", Arguments: map[string]any{"text": "ping"}},
				},
			},
			{Content: "done"},
		},
	}
	registry := tooling.NewRegistry()
	registry.Register(echoTool{})

	store := session.NewMemoryStore()
	loop := Loop{
		Model:         model,
		ModelName:     "test-model",
		Tools:         registry,
		Sessions:      store,
		Context:       ContextBuilder{SystemPrompt: "You are an agent."},
		MaxIterations: 4,
	}

	got, err := loop.Process(context.Background(), "s1", "run")
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if got != "done" {
		t.Fatalf("Process() = %q, want %q", got, "done")
	}

	history := store.GetHistory("s1")
	if len(history) != 4 {
		t.Fatalf("history length = %d, want 4", len(history))
	}
	if history[2].Role != "tool" || history[2].Content != "echo:ping" {
		t.Fatalf("tool history = %+v, want tool echo result", history[2])
	}
}

func TestLoopEmitsStreamingEvents(t *testing.T) {
	model := &streamingScriptedModel{
		response: provider.Response{Content: "hello"},
		chunks:   []string{"he", "llo"},
	}
	store := session.NewMemoryStore()
	bus := NewEventBus()
	defer bus.Close()
	sub := bus.Subscribe(16)
	defer bus.Unsubscribe(sub.ID)

	loop := Loop{
		Model:    model,
		Sessions: store,
		Context:  ContextBuilder{SystemPrompt: "You are an agent."},
		Events:   bus,
	}

	got, err := loop.Process(context.Background(), "s1", "hi")
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if got != "hello" {
		t.Fatalf("Process() = %q, want hello", got)
	}

	var deltas []string
drain:
	for {
		select {
		case evt := <-sub.C:
			if evt.Kind == EventModelDelta {
				payload, ok := evt.Payload.(ModelDeltaPayload)
				if !ok {
					t.Fatalf("payload type = %T, want ModelDeltaPayload", evt.Payload)
				}
				deltas = append(deltas, payload.Delta)
			}
		default:
			break drain
		}
	}

	if len(deltas) != 2 || deltas[0] != "he" || deltas[1] != "llo" {
		t.Fatalf("deltas = %#v, want [he llo]", deltas)
	}
}

func TestLoopEmitsReasoningEvents(t *testing.T) {
	model := &streamingScriptedModel{
		response: provider.Response{Content: "hello"},
		chunks:   []string{"think ", "done"},
		kinds: []provider.StreamChunkKind{
			provider.StreamChunkKindReasoning,
			provider.StreamChunkKindReasoning,
		},
	}
	store := session.NewMemoryStore()
	bus := NewEventBus()
	defer bus.Close()
	sub := bus.Subscribe(16)
	defer bus.Unsubscribe(sub.ID)

	loop := Loop{
		Model:    model,
		Sessions: store,
		Context:  ContextBuilder{SystemPrompt: "You are an agent."},
		Events:   bus,
	}

	_, err := loop.Process(context.Background(), "s1", "hi")
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}

	var deltas []string
drain:
	for {
		select {
		case evt := <-sub.C:
			if evt.Kind == EventModelReasoning {
				payload, ok := evt.Payload.(ModelReasoningPayload)
				if !ok {
					t.Fatalf("payload type = %T, want ModelReasoningPayload", evt.Payload)
				}
				deltas = append(deltas, payload.Delta)
			}
		default:
			break drain
		}
	}

	if len(deltas) != 2 || deltas[0] != "think " || deltas[1] != "done" {
		t.Fatalf("deltas = %#v, want [think  done]", deltas)
	}
}

func TestLoopEmitsTypedMetadata(t *testing.T) {
	model := &scriptedModel{
		responses: []*provider.Response{{Content: "hello"}},
	}
	store := session.NewMemoryStore()
	bus := NewEventBus()
	defer bus.Close()
	sub := bus.Subscribe(16)
	defer bus.Unsubscribe(sub.ID)

	loop := Loop{
		Model:    model,
		Sessions: store,
		Context:  ContextBuilder{SystemPrompt: "You are an agent."},
		Events:   bus,
	}

	_, err := loop.Process(context.Background(), "s1", "hi")
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}

	var sawTurnStart bool
	var sawModelRequest bool
drain:
	for {
		select {
		case evt := <-sub.C:
			if evt.Meta.SchemaVersion != EventSchemaVersion {
				t.Fatalf("schema version = %d, want %d", evt.Meta.SchemaVersion, EventSchemaVersion)
			}
			if evt.Meta.TurnID == "" {
				t.Fatalf("turn id is empty for event %s", evt.Kind)
			}
			switch evt.Kind {
			case EventTurnStarted:
				if _, ok := evt.Payload.(TurnStartedPayload); !ok {
					t.Fatalf("turn start payload = %T, want TurnStartedPayload", evt.Payload)
				}
				sawTurnStart = true
			case EventModelRequest:
				if _, ok := evt.Payload.(ModelRequestPayload); !ok {
					t.Fatalf("model request payload = %T, want ModelRequestPayload", evt.Payload)
				}
				sawModelRequest = true
			}
		default:
			break drain
		}
	}

	if !sawTurnStart || !sawModelRequest {
		t.Fatalf("expected typed events, got turnStart=%v modelRequest=%v", sawTurnStart, sawModelRequest)
	}
}

func TestLoopStopsWhenToolRequiresApproval(t *testing.T) {
	model := &scriptedModel{
		responses: []*provider.Response{
			{
				Content: "I need approval.",
				ToolCalls: []provider.ToolCall{
					{ID: "call-1", Name: "dangerous", Arguments: map[string]any{}},
				},
			},
		},
	}
	registry := tooling.NewRegistry()
	registry.Register(approvalTool{})

	store := session.NewMemoryStore()
	bus := NewEventBus()
	defer bus.Close()
	sub := bus.Subscribe(32)
	defer bus.Unsubscribe(sub.ID)

	loop := Loop{
		Model:         model,
		Tools:         registry,
		Sessions:      store,
		Context:       ContextBuilder{SystemPrompt: "You are an agent."},
		MaxIterations: 4,
		Events:        bus,
	}

	_, err := loop.Process(context.Background(), "s1", "run it")
	if err == nil {
		t.Fatal("Process() error = nil, want approval error")
	}
	approvalErr, ok := err.(*ApprovalRequiredError)
	if !ok {
		t.Fatalf("error = %T, want *ApprovalRequiredError", err)
	}
	if approvalErr.Request.ID != "approval-1" {
		t.Fatalf("approval request id = %q, want approval-1", approvalErr.Request.ID)
	}

	var sawApproval bool
	var sawTurnFinished bool
drain:
	for {
		select {
		case evt := <-sub.C:
			switch evt.Kind {
			case EventApprovalRequested:
				payload, ok := evt.Payload.(ApprovalRequestedPayload)
				if !ok {
					t.Fatalf("payload type = %T, want ApprovalRequestedPayload", evt.Payload)
				}
				if payload.RequestID != "approval-1" {
					t.Fatalf("request id = %q, want approval-1", payload.RequestID)
				}
				sawApproval = true
			case EventTurnFinished:
				payload, ok := evt.Payload.(TurnFinishedPayload)
				if !ok {
					t.Fatalf("payload type = %T, want TurnFinishedPayload", evt.Payload)
				}
				if payload.Status != TurnStatusApprovalRequired {
					t.Fatalf("turn status = %q, want %q", payload.Status, TurnStatusApprovalRequired)
				}
				sawTurnFinished = true
			}
		default:
			break drain
		}
	}

	if !sawApproval || !sawTurnFinished {
		t.Fatalf("expected approval flow events, got approval=%v turnFinished=%v", sawApproval, sawTurnFinished)
	}
}

func TestLoopContinuesAfterApproval(t *testing.T) {
	model := &scriptedModel{
		responses: []*provider.Response{
			{
				Content: "I need approval.",
				ToolCalls: []provider.ToolCall{
					{ID: "call-1", Name: "dangerous", Arguments: map[string]any{}},
				},
			},
			{Content: "done"},
		},
	}
	registry := tooling.NewRegistry()
	registry.Register(approvalTool{})

	store := session.NewMemoryStore()
	bus := NewEventBus()
	defer bus.Close()
	sub := bus.Subscribe(32)
	defer bus.Unsubscribe(sub.ID)

	loop := Loop{
		Model:         model,
		Tools:         registry,
		Sessions:      store,
		Context:       ContextBuilder{SystemPrompt: "You are an agent."},
		MaxIterations: 4,
		Events:        bus,
		Approval: func(_ context.Context, req tooling.ApprovalRequest) (bool, error) {
			return req.Tool == "dangerous", nil
		},
	}

	got, err := loop.Process(context.Background(), "s1", "run it")
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if got != "done" {
		t.Fatalf("Process() = %q, want done", got)
	}

	var sawResolved bool
drain:
	for {
		select {
		case evt := <-sub.C:
			if evt.Kind != EventApprovalResolved {
				continue
			}
			payload, ok := evt.Payload.(ApprovalResolvedPayload)
			if !ok {
				t.Fatalf("payload type = %T, want ApprovalResolvedPayload", evt.Payload)
			}
			if !payload.Approved {
				t.Fatalf("approved = false, want true")
			}
			sawResolved = true
		default:
			break drain
		}
	}
	if !sawResolved {
		t.Fatal("expected approval_resolved event")
	}
}

func TestLoopReturnsApprovalDeniedError(t *testing.T) {
	model := &scriptedModel{
		responses: []*provider.Response{
			{
				Content: "I need approval.",
				ToolCalls: []provider.ToolCall{
					{ID: "call-1", Name: "dangerous", Arguments: map[string]any{}},
				},
			},
		},
	}
	registry := tooling.NewRegistry()
	registry.Register(approvalTool{})

	store := session.NewMemoryStore()
	loop := Loop{
		Model:         model,
		Tools:         registry,
		Sessions:      store,
		Context:       ContextBuilder{SystemPrompt: "You are an agent."},
		MaxIterations: 4,
		Approval: func(_ context.Context, _ tooling.ApprovalRequest) (bool, error) {
			return false, nil
		},
	}

	_, err := loop.Process(context.Background(), "s1", "run it")
	if err == nil {
		t.Fatal("Process() error = nil, want approval denied error")
	}
	if _, ok := err.(*ApprovalDeniedError); !ok {
		t.Fatalf("error = %T, want *ApprovalDeniedError", err)
	}
}

func TestLoopCanContinueAfterReadFileEscapeApproval(t *testing.T) {
	root := t.TempDir()
	outsideRoot := t.TempDir()
	targetPath := filepath.Join(outsideRoot, "note.txt")
	if err := os.WriteFile(targetPath, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	model := &scriptedModel{
		responses: []*provider.Response{
			{
				Content: "I need to read a file.",
				ToolCalls: []provider.ToolCall{
					{ID: "call-1", Name: "read_file", Arguments: map[string]any{"path": targetPath}},
				},
			},
			{Content: "done"},
		},
	}
	registry := tooling.NewRegistry()
	registry.Register(builtintools.ReadFileTool{
		PathPolicy: builtintools.PathPolicy{
			Scope: builtintools.PathScopeWorkspace,
			Roots: []string{root},
		},
	})

	loop := Loop{
		Model:         model,
		Tools:         registry,
		Sessions:      session.NewMemoryStore(),
		Context:       ContextBuilder{SystemPrompt: "You are an agent."},
		MaxIterations: 4,
		Approval: func(_ context.Context, req tooling.ApprovalRequest) (bool, error) {
			return req.Tool == "read_file", nil
		},
	}

	got, err := loop.Process(context.Background(), "s1", "run it")
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if got != "done" {
		t.Fatalf("Process() = %q, want done", got)
	}
}
