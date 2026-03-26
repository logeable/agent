package agent

import "time"

const EventSchemaVersion = 1

// EventKind identifies a runtime event emitted by the loop.
//
// Why:
// Event kinds are the stable "verb vocabulary" of the runtime. We want new
// integrations to be able to key off a small, explicit set of event names.
type EventKind string

const (
	EventTurnStarted   EventKind = "turn_started"
	EventTurnFinished  EventKind = "turn_finished"
	EventModelRequest  EventKind = "model_request"
	EventModelDelta    EventKind = "model_delta"
	EventModelResponse EventKind = "model_response"
	EventToolStarted   EventKind = "tool_started"
	EventToolFinished  EventKind = "tool_finished"
	EventError         EventKind = "error"
)

// EventMeta carries stable correlation fields shared by every runtime event.
//
// Why:
// This is the long-term foundation for extensibility.
// Payloads can evolve per event kind, but correlation fields should remain
// stable so logs, UIs, and trace tools do not break every time a new feature
// is added to the loop.
type EventMeta struct {
	SchemaVersion int
	AgentID       string
	TurnID        string
	ParentTurnID  string
	SessionKey    string
	Iteration     int
	ToolCallID    string
	Source        string
}

func (m EventMeta) withIteration(iteration int) EventMeta {
	m.Iteration = iteration
	return m
}

func (m EventMeta) withToolCallID(toolCallID string) EventMeta {
	m.ToolCallID = toolCallID
	return m
}

// Event is the normalized envelope broadcast by the EventBus.
//
// What:
// The envelope is stable and payloads are event-specific structs.
//
// Why:
// This gives us the best of both worlds:
// - stable top-level metadata
// - strong typing per event kind
// - no dependence on unstructured `map[string]any` protocols
type Event struct {
	Kind    EventKind
	Time    time.Time
	Meta    EventMeta
	Payload any
}

// TurnStatus describes how a turn ended.
type TurnStatus string

const (
	TurnStatusCompleted TurnStatus = "completed"
	TurnStatusError     TurnStatus = "error"
)

// TurnStartedPayload describes the start of a user turn.
type TurnStartedPayload struct {
	UserMessage string
}

// TurnFinishedPayload describes the end of a user turn.
type TurnFinishedPayload struct {
	Status       TurnStatus
	FinalContent string
}

// ModelRequestPayload describes an outbound model call.
type ModelRequestPayload struct {
	Model         string
	MessagesCount int
	ToolsCount    int
	Streaming     bool
}

// ModelDeltaPayload describes one incremental streamed model chunk.
type ModelDeltaPayload struct {
	Delta       string
	Accumulated string
}

// ModelResponsePayload describes a completed model response.
type ModelResponsePayload struct {
	ContentLen int
	ToolCalls  int
}

// ToolStartedPayload describes a tool invocation request.
type ToolStartedPayload struct {
	Tool      string
	Arguments map[string]any
}

// ToolFinishedPayload describes the outcome of a tool invocation.
type ToolFinishedPayload struct {
	Tool        string
	ForModel    string
	IsError     bool
	UserPreview string
}

// ErrorPayload describes an execution error at some stage of the loop.
type ErrorPayload struct {
	Stage   string
	Message string
}
