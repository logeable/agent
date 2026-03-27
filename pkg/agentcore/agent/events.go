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
	EventTurnStarted       EventKind = "turn_started"
	EventTurnFinished      EventKind = "turn_finished"
	EventModelRequest      EventKind = "model_request"
	EventModelDelta        EventKind = "model_delta"
	EventModelReasoning    EventKind = "model_reasoning"
	EventModelResponse     EventKind = "model_response"
	EventToolStarted       EventKind = "tool_started"
	EventToolFinished      EventKind = "tool_finished"
	EventApprovalRequested EventKind = "approval_requested"
	EventApprovalResolved  EventKind = "approval_resolved"
	EventError             EventKind = "error"
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
	TurnStatusCompleted        TurnStatus = "completed"
	TurnStatusError            TurnStatus = "error"
	TurnStatusApprovalRequired TurnStatus = "approval_required"
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

// ModelReasoningPayload describes one incremental streamed reasoning chunk.
//
// Why:
// Reasoning text is not the same as final assistant output. Keeping it in a
// separate event lets hosts decide whether to hide it, stream it, summarize
// it, or show it in a distinct UI treatment.
type ModelReasoningPayload struct {
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
	ErrorText   string
	Metadata    map[string]any
}

// ApprovalRequestedPayload describes a tool action that requires approval
// before execution can continue.
type ApprovalRequestedPayload struct {
	Tool        string
	RequestID   string
	Reason      string
	ActionLabel string
	Details     map[string]any
}

// ApprovalResolvedPayload describes the user's or host's decision for a prior
// approval request.
type ApprovalResolvedPayload struct {
	Tool      string
	RequestID string
	Approved  bool
	Reason    string
}

// ErrorPayload describes an execution error at some stage of the loop.
type ErrorPayload struct {
	Stage   string
	Message string
}
