package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/logeable/agent/pkg/agentcore/provider"
	"github.com/logeable/agent/pkg/agentcore/session"
	"github.com/logeable/agent/pkg/agentcore/tooling"
)

// DefaultMaxIterations is the safety cap for repeated model->tool->model loops.
//
// Why:
// Agent loops can get stuck if the model keeps requesting tools forever.
// A hard limit makes failure mode obvious and prevents runaway execution.
const DefaultMaxIterations = 8

// ApprovalRequiredError reports that a turn stopped because a tool requested
// explicit approval before continuing.
//
// Why:
// This is not the same as a normal tool failure. The action may be valid, but
// the runtime intentionally pauses instead of executing it immediately.
type ApprovalRequiredError struct {
	Request tooling.ApprovalRequest
}

func (e *ApprovalRequiredError) Error() string {
	if e == nil {
		return "approval required"
	}
	if e.Request.Reason != "" {
		return e.Request.Reason
	}
	if e.Request.Tool != "" {
		return fmt.Sprintf("approval required for tool %q", e.Request.Tool)
	}
	return "approval required"
}

// ApprovalDeniedError reports that an approval-aware loop requested approval,
// received an explicit denial, and therefore could not continue execution.
type ApprovalDeniedError struct {
	Request tooling.ApprovalRequest
}

func (e *ApprovalDeniedError) Error() string {
	if e == nil {
		return "approval denied"
	}
	if e.Request.Reason != "" {
		return fmt.Sprintf("approval denied: %s", e.Request.Reason)
	}
	if e.Request.Tool != "" {
		return fmt.Sprintf("approval denied for tool %q", e.Request.Tool)
	}
	return "approval denied"
}

// ApprovalHandler lets the host decide whether a requested action may proceed.
//
// Why:
// Approval is an execution concern, but the actual decision belongs to the
// outer host (CLI, UI, API server), not to the loop or the tool itself.
type ApprovalHandler func(ctx context.Context, req tooling.ApprovalRequest) (approved bool, err error)

// Loop is the extracted "agent runtime" in its smallest useful form.
//
// What:
// It ties together:
// - a model
// - a session store
// - a context builder
// - a tool registry
//
// Why:
// These are the minimum moving parts needed for tool-using agent behavior.
type Loop struct {
	Model         provider.ChatModel
	AgentID       string
	ModelName     string
	Tools         *tooling.Registry
	Sessions      session.Store
	Context       ContextBuilder
	MaxIterations int
	Options       map[string]any
	ShowReasoning bool
	Events        *EventBus
	Approval      ApprovalHandler
	nextTurnID    atomic.Uint64
	closeMu       sync.Mutex
	closers       []func() error
}

// Process executes one user turn.
//
// High-level flow:
// 1. Load history and build model input
// 2. Save the user message into session memory
// 3. Ask the model what to do
// 4. If the model asks for tools, execute them and feed results back
// 5. Stop when the model finally returns plain content
//
// Why:
// This is the central "agent loop" idea extracted from PicoClaw.
func (l *Loop) Process(ctx context.Context, sessionKey, userMessage string) (string, error) {
	if l.Model == nil {
		return "", fmt.Errorf("model is nil")
	}
	if l.Sessions == nil {
		return "", fmt.Errorf("session store is nil")
	}
	if l.Tools == nil {
		l.Tools = tooling.NewRegistry()
	}

	maxIterations := l.MaxIterations
	if maxIterations <= 0 {
		maxIterations = DefaultMaxIterations
	}

	turnID := fmt.Sprintf("turn-%d", l.nextTurnID.Add(1))
	turnMeta := EventMeta{
		SchemaVersion: EventSchemaVersion,
		AgentID:       l.resolvedAgentID(),
		TurnID:        turnID,
		SessionKey:    sessionKey,
		Source:        "agent.Loop.Process",
	}

	l.emit(turnMeta.withIteration(0), EventTurnStarted, TurnStartedPayload{
		UserMessage: userMessage,
	})

	// Build the first model input from long-term session state plus the new message.
	history := l.Sessions.GetHistory(sessionKey)
	summary := l.Sessions.GetSummary(sessionKey)
	messages := l.Context.BuildMessages(history, summary, userMessage)

	// Persist the user message immediately.
	//
	// Why:
	// The session transcript should reflect what the user asked even if a later
	// model call fails. This also keeps the storage behavior obvious for readers.
	l.Sessions.AddMessage(sessionKey, "user", userMessage)

	finalStatus := TurnStatusCompleted
	finalContent := ""
	defer func() {
		l.emit(turnMeta.withIteration(0), EventTurnFinished, TurnFinishedPayload{
			Status:       finalStatus,
			FinalContent: finalContent,
		})
	}()

	for iteration := 0; iteration < maxIterations; iteration++ {
		meta := turnMeta.withIteration(iteration + 1)
		_, streaming := l.Model.(provider.StreamingChatModel)
		l.emit(meta, EventModelRequest, ModelRequestPayload{
			Model:         l.ModelName,
			MessagesCount: len(messages),
			ToolsCount:    len(l.Tools.Definitions()),
			Streaming:     streaming,
		})

		// Each pass asks the model either for:
		// - a final answer, or
		// - one or more tool calls.
		response, err := l.callModel(ctx, meta, messages)
		if err != nil {
			finalStatus = TurnStatusError
			l.emit(meta, EventError, ErrorPayload{
				Stage:   "model_call",
				Message: err.Error(),
			})
			return "", err
		}
		if response == nil {
			finalStatus = TurnStatusError
			l.emit(meta, EventError, ErrorPayload{
				Stage:   "model_call",
				Message: "model returned nil response",
			})
			return "", fmt.Errorf("model returned nil response")
		}

		l.emit(meta, EventModelResponse, ModelResponsePayload{
			ContentLen: len(response.Content),
			ToolCalls:  len(response.ToolCalls),
		})

		// No tool calls means the model considers the task finished.
		if len(response.ToolCalls) == 0 {
			l.Sessions.AddMessage(sessionKey, "assistant", response.Content)
			finalContent = response.Content
			return response.Content, nil
		}

		// Record the assistant message that requested tools.
		//
		// Why:
		// In a real agent transcript, this is an important part of history:
		// it explains why the following tool messages exist.
		assistant := provider.Message{
			Role:      "assistant",
			Content:   response.Content,
			ToolCalls: response.ToolCalls,
		}
		messages = append(messages, assistant)
		l.Sessions.AddFullMessage(sessionKey, assistant)

		// Execute each requested tool and append its result as a `tool` message.
		//
		// Why:
		// The next model call needs to see the tool outputs so it can decide what
		// to do next: answer the user, call another tool, or recover from an error.
		for _, call := range response.ToolCalls {
			toolMeta := meta.withToolCallID(call.ID)
			l.emit(toolMeta, EventToolStarted, ToolStartedPayload{
				Tool:      call.Name,
				Arguments: call.Arguments,
			})
			result := l.Tools.Execute(ctx, call.Name, call.Arguments)
			if result != nil && result.Approval != nil {
				req := *result.Approval
				if req.Tool == "" {
					req.Tool = call.Name
				}
				l.emit(toolMeta, EventApprovalRequested, ApprovalRequestedPayload{
					Tool:        req.Tool,
					RequestID:   req.ID,
					Reason:      req.Reason,
					ActionLabel: req.ActionLabel,
					Details:     req.Details,
				})

				if l.Approval == nil {
					finalStatus = TurnStatusApprovalRequired
					return "", &ApprovalRequiredError{Request: req}
				}

				approved, approvalErr := l.Approval(ctx, req)
				if approvalErr != nil {
					finalStatus = TurnStatusError
					l.emit(toolMeta, EventError, ErrorPayload{
						Stage:   "approval",
						Message: approvalErr.Error(),
					})
					return "", approvalErr
				}
				l.emit(toolMeta, EventApprovalResolved, ApprovalResolvedPayload{
					Tool:      req.Tool,
					RequestID: req.ID,
					Approved:  approved,
					Reason:    req.Reason,
				})
				if !approved {
					finalStatus = TurnStatusApprovalRequired
					return "", &ApprovalDeniedError{Request: req}
				}

				result = l.Tools.Execute(tooling.ContextWithApprovedTool(ctx, req.Tool), call.Name, call.Arguments)
			}
			toolMsg := provider.Message{
				Role:       "tool",
				Content:    result.ContentForModel(),
				ToolCallID: call.ID,
			}
			messages = append(messages, toolMsg)
			l.Sessions.AddFullMessage(sessionKey, toolMsg)
			l.emit(toolMeta, EventToolFinished, ToolFinishedPayload{
				Tool:        call.Name,
				ForModel:    toolMsg.Content,
				IsError:     result.IsError,
				UserPreview: result.ForUser,
				ErrorText:   errorText(result),
				Metadata:    result.Metadata,
			})
		}
	}

	// Hitting the cap means the loop never converged to a final answer.
	finalStatus = TurnStatusError
	l.emit(turnMeta.withIteration(maxIterations), EventError, ErrorPayload{
		Stage:   "max_iterations",
		Message: fmt.Sprintf("max iterations exceeded (%d)", maxIterations),
	})
	return "", fmt.Errorf("max iterations exceeded")
}

func (l *Loop) callModel(
	ctx context.Context,
	meta EventMeta,
	messages []provider.Message,
) (*provider.Response, error) {
	if streamingModel, ok := l.Model.(provider.StreamingChatModel); ok {
		return streamingModel.ChatStream(
			ctx,
			messages,
			l.Tools.Definitions(),
			l.ModelName,
			l.Options,
			func(chunk provider.StreamChunk) {
				switch chunk.Kind {
				case provider.StreamChunkKindReasoning:
					if !l.ShowReasoning {
						return
					}
					l.emit(meta, EventModelReasoning, ModelReasoningPayload{
						Delta:       chunk.Delta,
						Accumulated: chunk.Accumulated,
					})
				default:
					l.emit(meta, EventModelDelta, ModelDeltaPayload{
						Delta:       chunk.Delta,
						Accumulated: chunk.Accumulated,
					})
				}
			},
		)
	}
	return l.Model.Chat(ctx, messages, l.Tools.Definitions(), l.ModelName, l.Options)
}

func (l *Loop) emit(meta EventMeta, kind EventKind, payload any) {
	if l.Events == nil {
		return
	}
	l.Events.Emit(Event{
		Kind:    kind,
		Meta:    meta,
		Payload: payload,
	})
}

func (l *Loop) resolvedAgentID() string {
	if l.AgentID != "" {
		return l.AgentID
	}
	return "main"
}

// AddCloser registers a cleanup function to run when the host is done with the
// loop.
//
// Why:
// Most core execution turns are pure in-memory operations, but bridge layers
// such as MCP may allocate external resources like child processes or network
// sessions. The loop needs one narrow hook for releasing those resources
// without hard-coding knowledge of higher-level integrations.
func (l *Loop) AddCloser(fn func() error) {
	if fn == nil {
		return
	}
	l.closeMu.Lock()
	l.closers = append(l.closers, fn)
	l.closeMu.Unlock()
}

// Close runs all registered cleanup functions.
func (l *Loop) Close() error {
	l.closeMu.Lock()
	closers := append([]func() error(nil), l.closers...)
	l.closers = nil
	l.closeMu.Unlock()

	var firstErr error
	for i := len(closers) - 1; i >= 0; i-- {
		if err := closers[i](); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// EncodeToolArguments is a tiny debugging helper.
//
// Why:
// When inspecting traces or writing tests, it is useful to serialize tool
// arguments into a stable string without repeating JSON marshaling code.
func EncodeToolArguments(arguments map[string]any) string {
	data, err := json.Marshal(arguments)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func errorText(result *tooling.Result) string {
	if result == nil || result.Err == nil {
		return ""
	}
	return result.Err.Error()
}
