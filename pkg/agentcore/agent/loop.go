package agent

import (
	"context"
	"encoding/json"
	"fmt"
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
	Events        *EventBus
	nextTurnID    atomic.Uint64
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
			})
		}
	}

	// Hitting the cap means the loop never converged to a final answer.
	finalStatus = TurnStatusError
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
				l.emit(meta, EventModelDelta, ModelDeltaPayload{
					Delta:       chunk.Delta,
					Accumulated: chunk.Accumulated,
				})
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
