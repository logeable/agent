package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/logeable/agent/pkg/agentcore/compaction"
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

type capturingModel struct {
	response provider.Response
	calls    [][]provider.Message
}

func (m *capturingModel) Chat(
	_ context.Context,
	messages []provider.Message,
	_ []provider.ToolDefinition,
	_ string,
	_ map[string]any,
) (*provider.Response, error) {
	snapshot := append([]provider.Message(nil), messages...)
	m.calls = append(m.calls, snapshot)
	return &m.response, nil
}

type stepCapturingModel struct {
	responses []*provider.Response
	calls     [][]provider.Message
}

func (m *stepCapturingModel) Chat(
	_ context.Context,
	messages []provider.Message,
	_ []provider.ToolDefinition,
	_ string,
	_ map[string]any,
) (*provider.Response, error) {
	if len(m.calls) >= len(m.responses) {
		return nil, fmt.Errorf("unexpected extra model call")
	}
	snapshot := append([]provider.Message(nil), messages...)
	m.calls = append(m.calls, snapshot)
	return m.responses[len(m.calls)-1], nil
}

type flakyModel struct {
	responses []*provider.Response
	errs      []error
	calls     [][]provider.Message
}

func (m *flakyModel) Chat(
	_ context.Context,
	messages []provider.Message,
	_ []provider.ToolDefinition,
	_ string,
	_ map[string]any,
) (*provider.Response, error) {
	snapshot := append([]provider.Message(nil), messages...)
	m.calls = append(m.calls, snapshot)
	idx := len(m.calls) - 1
	if idx < len(m.errs) && m.errs[idx] != nil {
		return nil, m.errs[idx]
	}
	if idx < len(m.responses) {
		return m.responses[idx], nil
	}
	return nil, fmt.Errorf("unexpected extra model call")
}

type overflowThenScriptedModel struct {
	err      error
	response []*provider.Response
	calls    [][]provider.Message
}

func (m *overflowThenScriptedModel) Chat(
	_ context.Context,
	messages []provider.Message,
	_ []provider.ToolDefinition,
	_ string,
	_ map[string]any,
) (*provider.Response, error) {
	snapshot := append([]provider.Message(nil), messages...)
	m.calls = append(m.calls, snapshot)
	callIdx := len(m.calls) - 1
	if callIdx == 0 && m.err != nil {
		return nil, m.err
	}
	respIdx := callIdx
	if m.err != nil {
		respIdx--
	}
	if respIdx >= 0 && respIdx < len(m.response) {
		return m.response[respIdx], nil
	}
	return nil, fmt.Errorf("unexpected extra model call")
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

type nthCompactCompactor struct {
	compactOnCall int
	calls         int
}

func (c *nthCompactCompactor) Compact(input compaction.ContextCompactInput) compaction.ContextCompactResult {
	c.calls++
	if c.calls != c.compactOnCall || len(input.Messages) < 2 {
		return compaction.ContextCompactResult{
			Messages: input.Messages,
			Strategy: "keep_all",
		}
	}
	compacted := []provider.Message{input.Messages[0], input.Messages[len(input.Messages)-1]}
	return compaction.ContextCompactResult{
		Messages:        compacted,
		Strategy:        "nth_trim",
		DroppedMessages: len(input.Messages) - len(compacted),
	}
}

type estimatorFunc func([]provider.Message) int

func (f estimatorFunc) EstimateMessages(messages []provider.Message) int {
	return f(messages)
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

func TestLoopStopsOnRepeatedToolCalls(t *testing.T) {
	model := &scriptedModel{
		responses: []*provider.Response{
			{
				Content: "Inspecting.",
				ToolCalls: []provider.ToolCall{
					{ID: "call-1", Name: "echo", Arguments: map[string]any{"text": "ping"}},
				},
			},
			{
				Content: "Inspecting again.",
				ToolCalls: []provider.ToolCall{
					{ID: "call-2", Name: "echo", Arguments: map[string]any{"text": "ping"}},
				},
			},
		},
	}
	registry := tooling.NewRegistry()
	registry.Register(echoTool{})

	bus := NewEventBus()
	defer bus.Close()
	sub := bus.Subscribe(16)
	defer bus.Unsubscribe(sub.ID)

	loop := Loop{
		Model:         model,
		ModelName:     "test-model",
		Tools:         registry,
		Sessions:      session.NewMemoryStore(),
		Context:       ContextBuilder{SystemPrompt: "You are an agent."},
		MaxIterations: 4,
		Events:        bus,
	}

	_, err := loop.Process(context.Background(), "s1", "run")
	if err == nil {
		t.Fatal("Process() error = nil, want repeated tool call error")
	}
	if got := err.Error(); got != "model repeated the same tool calls without converging" {
		t.Fatalf("error = %q, want repeated tool call error", got)
	}

	var sawRepeatedError bool
drain:
	for {
		select {
		case evt := <-sub.C:
			if evt.Kind != EventError {
				continue
			}
			payload, ok := evt.Payload.(ErrorPayload)
			if !ok {
				t.Fatalf("payload type = %T, want ErrorPayload", evt.Payload)
			}
			if payload.Stage == "repeated_tool_calls" {
				sawRepeatedError = true
			}
		default:
			break drain
		}
	}

	if !sawRepeatedError {
		t.Fatal("expected repeated_tool_calls error event")
	}
}

func TestLoopContextBudgetDoesNotOverwriteAccumulatedMessages(t *testing.T) {
	model := &stepCapturingModel{
		responses: []*provider.Response{
			{
				ToolCalls: []provider.ToolCall{
					{ID: "call-1", Name: "echo", Arguments: map[string]any{"text": "one"}},
				},
			},
			{
				ToolCalls: []provider.ToolCall{
					{ID: "call-2", Name: "echo", Arguments: map[string]any{"text": "two"}},
				},
			},
			{Content: "done"},
		},
	}
	registry := tooling.NewRegistry()
	registry.Register(echoTool{})

	compactor := &nthCompactCompactor{compactOnCall: 2}
	loop := Loop{
		Model:    model,
		Tools:    registry,
		Sessions: session.NewMemoryStore(),
		Context:  ContextBuilder{SystemPrompt: "You are an agent."},
		ContextBudget: ContextBudget{
			MaxInputTokens: 1,
			TargetFraction: 1,
			Estimator: estimatorFunc(func(messages []provider.Message) int {
				return 10
			}),
			Compactor: compactor,
		},
		MaxIterations: 4,
	}

	got, err := loop.Process(context.Background(), "s1", "run")
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if got != "done" {
		t.Fatalf("Process() = %q, want done", got)
	}
	if len(model.calls) != 3 {
		t.Fatalf("model calls = %d, want 3", len(model.calls))
	}

	thirdCall := model.calls[2]
	var sawFirstToolResult bool
	for _, msg := range thirdCall {
		if msg.Role == "tool" && msg.Content == "echo:one" {
			sawFirstToolResult = true
			break
		}
	}
	if !sawFirstToolResult {
		t.Fatalf("third call messages = %+v, want preserved first tool result", thirdCall)
	}
}

func TestRecentMessageCompactorKeepsToolTransactionsAtomic(t *testing.T) {
	compactor := compaction.RecentMessageCompactor{}
	messages := []provider.Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "older user"},
		{
			Role:    "assistant",
			Content: "calling tool",
			ToolCalls: []provider.ToolCall{{
				ID:        "call-1",
				Name:      "echo",
				Arguments: map[string]any{"text": "hello"},
			}},
		},
		{Role: "tool", ToolCallID: "call-1", Content: "echo:hello"},
		{Role: "user", Content: "latest user"},
	}

	result := compactor.Compact(compaction.ContextCompactInput{
		Messages:     messages,
		TargetTokens: 40,
	})

	var sawAssistantCall bool
	var sawToolOutput bool
	for _, msg := range result.Messages {
		if len(msg.ToolCalls) > 0 {
			sawAssistantCall = true
		}
		if msg.Role == "tool" && msg.ToolCallID == "call-1" {
			sawToolOutput = true
		}
	}
	if sawAssistantCall != sawToolOutput {
		t.Fatalf("compacted messages = %+v, want assistant tool call and tool output kept or dropped together", result.Messages)
	}
}

func TestRecentMessageCompactorDropsWholeToolTransactionWhenNeeded(t *testing.T) {
	compactor := compaction.RecentMessageCompactor{}
	messages := []provider.Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "older user"},
		{
			Role:    "assistant",
			Content: "calling tool",
			ToolCalls: []provider.ToolCall{{
				ID:        "call-1",
				Name:      "echo",
				Arguments: map[string]any{"text": "hello"},
			}},
		},
		{Role: "tool", ToolCallID: "call-1", Content: "echo:hello"},
		{Role: "user", Content: "latest user"},
	}

	result := compactor.Compact(compaction.ContextCompactInput{
		Messages:     messages,
		TargetTokens: 10,
	})

	if len(result.Messages) != 2 {
		t.Fatalf("compacted messages len = %d, want 2", len(result.Messages))
	}
	if result.Messages[0].Role != "system" || result.Messages[1].Role != "user" || result.Messages[1].Content != "latest user" {
		t.Fatalf("compacted messages = %+v, want only system and latest user", result.Messages)
	}
}

func TestRecentMessageCompactorProducesValidResponsesToolHistory(t *testing.T) {
	compactor := compaction.RecentMessageCompactor{}
	messages := []provider.Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "older user"},
		{
			Role:    "assistant",
			Content: "Let me call a tool",
			ToolCalls: []provider.ToolCall{{
				ID:        "call-1",
				Name:      "echo",
				Arguments: map[string]any{"text": "hello"},
			}},
		},
		{Role: "tool", ToolCallID: "call-1", Content: "echo:hello"},
		{Role: "user", Content: "latest user"},
	}

	compacted := compactor.Compact(compaction.ContextCompactInput{
		Messages:     messages,
		TargetTokens: 40,
	}).Messages

	_, input := provider.SerializeResponsesInputForTest(compacted)
	var callIDs map[string]struct{} = make(map[string]struct{})
	var outputIDs map[string]struct{} = make(map[string]struct{})
	for _, item := range input {
		switch item["type"] {
		case "function_call":
			if id, ok := item["call_id"].(string); ok && id != "" {
				callIDs[id] = struct{}{}
			}
		case "function_call_output":
			if id, ok := item["call_id"].(string); ok && id != "" {
				outputIDs[id] = struct{}{}
			}
		}
	}
	for id := range callIDs {
		if _, ok := outputIDs[id]; !ok {
			t.Fatalf("serialized input = %+v, want output for function call %q", input, id)
		}
	}
}

func TestLoopRetriesRetryableModelErrors(t *testing.T) {
	model := &flakyModel{
		errs: []error{
			&provider.ModelError{Kind: provider.ModelErrorRetryable, Message: "temporary upstream failure"},
		},
		responses: []*provider.Response{
			nil,
			{Content: "done"},
		},
	}

	bus := NewEventBus()
	defer bus.Close()
	sub := bus.Subscribe(16)
	defer bus.Unsubscribe(sub.ID)

	loop := Loop{
		Model:    model,
		Sessions: session.NewMemoryStore(),
		Context:  ContextBuilder{SystemPrompt: "You are an agent."},
		Events:   bus,
	}

	got, err := loop.Process(context.Background(), "s1", "hi")
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if got != "done" {
		t.Fatalf("Process() = %q, want done", got)
	}
	if len(model.calls) != 2 {
		t.Fatalf("model calls = %d, want 2", len(model.calls))
	}

	var sawRetry bool
drain:
	for {
		select {
		case evt := <-sub.C:
			if evt.Kind != EventModelRetry {
				continue
			}
			payload, ok := evt.Payload.(ModelRetryPayload)
			if !ok {
				t.Fatalf("payload type = %T, want ModelRetryPayload", evt.Payload)
			}
			if payload.ErrorKind == string(provider.ModelErrorRetryable) {
				sawRetry = true
			}
		default:
			break drain
		}
	}
	if !sawRetry {
		t.Fatal("expected model_retry event")
	}
}

func TestLoopRetriesContextOverflowWithCompaction(t *testing.T) {
	store := session.NewMemoryStore()
	store.AddMessage("s1", "user", "older user message")
	store.AddMessage("s1", "assistant", "older assistant message")

	model := &flakyModel{
		errs: []error{
			&provider.ModelError{Kind: provider.ModelErrorContextOverflow, Message: "context too long"},
		},
		responses: []*provider.Response{
			nil,
			{Content: "done"},
		},
	}

	bus := NewEventBus()
	defer bus.Close()
	sub := bus.Subscribe(16)
	defer bus.Unsubscribe(sub.ID)

	loop := Loop{
		Model:    model,
		Sessions: store,
		Context:  ContextBuilder{SystemPrompt: "System prompt"},
		Events:   bus,
	}

	got, err := loop.Process(context.Background(), "s1", "latest question")
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if got != "done" {
		t.Fatalf("Process() = %q, want done", got)
	}
	if len(model.calls) != 2 {
		t.Fatalf("model calls = %d, want 2", len(model.calls))
	}
	if len(model.calls[1]) >= len(model.calls[0]) {
		t.Fatalf("retry call len = %d, want less than first call len %d", len(model.calls[1]), len(model.calls[0]))
	}
	last := model.calls[1][len(model.calls[1])-1]
	if last.Role != "user" || last.Content != "latest question" {
		t.Fatalf("last retry message = %+v, want latest user message", last)
	}

	var sawCompacted bool
drain:
	for {
		select {
		case evt := <-sub.C:
			if evt.Kind != EventContextCompacted {
				continue
			}
			payload, ok := evt.Payload.(ContextCompactedPayload)
			if !ok {
				t.Fatalf("payload type = %T, want ContextCompactedPayload", evt.Payload)
			}
			if payload.Strategy != "overflow_retry_trim" {
				t.Fatalf("strategy = %q, want overflow_retry_trim", payload.Strategy)
			}
			if payload.MessagesAfter >= payload.MessagesBefore {
				t.Fatalf("messages after = %d, want less than before %d", payload.MessagesAfter, payload.MessagesBefore)
			}
			sawCompacted = true
		default:
			break drain
		}
	}
	if !sawCompacted {
		t.Fatal("expected context_compacted event during overflow retry")
	}
}

func TestLoopPersistsCompactedActiveMessagesAfterOverflowRetry(t *testing.T) {
	store := session.NewMemoryStore()
	store.AddMessage("s1", "user", "older user message")
	store.AddMessage("s1", "assistant", "older assistant message")

	model := &overflowThenScriptedModel{
		err: &provider.ModelError{Kind: provider.ModelErrorContextOverflow, Message: "context too long"},
		response: []*provider.Response{
			{
				ToolCalls: []provider.ToolCall{
					{ID: "call-1", Name: "echo", Arguments: map[string]any{"text": "one"}},
				},
			},
			{Content: "done"},
		},
	}

	registry := tooling.NewRegistry()
	registry.Register(echoTool{})

	loop := Loop{
		Model:    model,
		Tools:    registry,
		Sessions: store,
		Context:  ContextBuilder{SystemPrompt: "System prompt"},
	}

	got, err := loop.Process(context.Background(), "s1", "latest question")
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if got != "done" {
		t.Fatalf("Process() = %q, want done", got)
	}
	if len(model.calls) != 3 {
		t.Fatalf("model calls = %d, want 3", len(model.calls))
	}
	thirdCall := model.calls[2]
	for _, msg := range thirdCall {
		if msg.Content == "older user message" || msg.Content == "older assistant message" {
			t.Fatalf("third call messages = %+v, want compacted active context without older messages", thirdCall)
		}
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

func TestLoopEmitsModelUsageEvents(t *testing.T) {
	model := &scriptedModel{
		responses: []*provider.Response{{
			Content: "hello",
			Usage: &provider.Usage{
				InputTokens:  21,
				OutputTokens: 9,
				TotalTokens:  30,
			},
		}},
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

	var sawUsage bool
drain:
	for {
		select {
		case evt := <-sub.C:
			if evt.Kind != EventModelUsage {
				continue
			}
			payload, ok := evt.Payload.(ModelUsagePayload)
			if !ok {
				t.Fatalf("payload type = %T, want ModelUsagePayload", evt.Payload)
			}
			if payload.InputTokens != 21 || payload.OutputTokens != 9 || payload.TotalTokens != 30 {
				t.Fatalf("payload = %+v, want input=21 output=9 total=30", payload)
			}
			sawUsage = true
		default:
			break drain
		}
	}

	if !sawUsage {
		t.Fatal("expected model_usage event")
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

func TestLoopCompactsActiveContextWithoutMutatingSession(t *testing.T) {
	store := session.NewMemoryStore()
	store.AddMessage("s1", "user", "older user message that is intentionally long to trigger compaction")
	store.AddMessage("s1", "assistant", "older assistant message that is also intentionally long to trigger compaction")
	store.AddMessage("s1", "user", "another older user message that should stay in session storage")

	model := &capturingModel{
		response: provider.Response{Content: "done"},
	}
	bus := NewEventBus()
	defer bus.Close()
	sub := bus.Subscribe(16)
	defer bus.Unsubscribe(sub.ID)

	loop := Loop{
		Model:    model,
		Sessions: store,
		Context: ContextBuilder{
			SystemPrompt: "System prompt that is long enough to consume budget on its own for this test.",
		},
		ContextBudget: ContextBudget{
			MaxInputTokens: 32,
			TargetFraction: 0.5,
		},
		Events: bus,
	}

	got, err := loop.Process(context.Background(), "s1", "latest question must remain visible")
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if got != "done" {
		t.Fatalf("Process() = %q, want done", got)
	}
	if len(model.calls) != 1 {
		t.Fatalf("model calls = %d, want 1", len(model.calls))
	}

	lastCall := model.calls[0]
	if len(lastCall) == 0 {
		t.Fatal("model call messages are empty")
	}
	lastMessage := lastCall[len(lastCall)-1]
	if lastMessage.Role != "user" || lastMessage.Content != "latest question must remain visible" {
		t.Fatalf("last model message = %+v, want latest user message", lastMessage)
	}

	history := store.GetHistory("s1")
	if len(history) != 5 {
		t.Fatalf("session history length = %d, want 5", len(history))
	}
	if history[0].Content != "older user message that is intentionally long to trigger compaction" {
		t.Fatalf("session history was rewritten: %+v", history)
	}

	var sawBudget bool
	var sawCompacted bool
drain:
	for {
		select {
		case evt := <-sub.C:
			switch evt.Kind {
			case EventContextBudget:
				payload, ok := evt.Payload.(ContextBudgetPayload)
				if !ok {
					t.Fatalf("payload type = %T, want ContextBudgetPayload", evt.Payload)
				}
				if !payload.TriggeredCompaction {
					t.Fatalf("TriggeredCompaction = false, want true")
				}
				if payload.EstimatedTokensBefore <= payload.BudgetTokens {
					t.Fatalf("estimated=%d budget=%d, want estimated > budget", payload.EstimatedTokensBefore, payload.BudgetTokens)
				}
				sawBudget = true
			case EventContextCompacted:
				payload, ok := evt.Payload.(ContextCompactedPayload)
				if !ok {
					t.Fatalf("payload type = %T, want ContextCompactedPayload", evt.Payload)
				}
				if payload.EstimatedTokensAfter >= payload.EstimatedTokensBefore {
					t.Fatalf("estimated after = %d, want less than before %d", payload.EstimatedTokensAfter, payload.EstimatedTokensBefore)
				}
				if payload.MessagesAfter >= payload.MessagesBefore {
					t.Fatalf("messages after = %d, want less than before %d", payload.MessagesAfter, payload.MessagesBefore)
				}
				sawCompacted = true
			}
		default:
			break drain
		}
	}

	if !sawBudget || !sawCompacted {
		t.Fatalf("expected budget telemetry, got budget=%v compacted=%v", sawBudget, sawCompacted)
	}
}
