package agent

import (
	"context"
	"fmt"
	"testing"

	"github.com/logeable/agent/pkg/agentcore/provider"
	"github.com/logeable/agent/pkg/agentcore/session"
	"github.com/logeable/agent/pkg/agentcore/tooling"
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
		onChunk(provider.StreamChunk{
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
