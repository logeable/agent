package provider

import "context"

// Message is the normalized unit exchanged between the agent loop and the model.
//
// What:
// A message represents one item in the conversation transcript:
// system prompt, user input, assistant output, or tool result.
//
// Why:
// The agent loop should not depend on any single LLM vendor's request format.
// By collapsing everything into one small internal shape, we can swap model
// adapters without rewriting the loop itself.
type Message struct {
	Role       string
	Content    string
	ToolCallID string
	ToolCalls  []ToolCall
}

// ToolCall describes one tool invocation requested by the model.
//
// What:
// The model can ask the runtime to call a named tool with structured arguments.
//
// Why:
// This is the key mechanism that turns a plain chatbot into an "agent":
// the model can decide to perform actions, observe results, and continue.
type ToolCall struct {
	ID        string
	Name      string
	Arguments map[string]any
}

// ToolDefinition is the schema we expose to the model before a call.
//
// What:
// It tells the model which tools exist, what they do, and what arguments they accept.
//
// Why:
// The loop needs a provider-agnostic way to advertise capabilities.
// Different providers serialize tool schemas differently, but the loop should
// speak one internal language.
type ToolDefinition struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// Response is the normalized output returned by a model call.
//
// What:
// A response may contain either final natural-language content, tool calls, or both.
//
// Why:
// Most agent loops are built around a repeated pattern:
// "call model -> maybe call tools -> call model again".
// This struct is the smallest useful shape for that pattern.
type Response struct {
	Content   string
	ToolCalls []ToolCall
}

type StreamChunkKind string

const (
	StreamChunkKindOutputText StreamChunkKind = "output_text"
	StreamChunkKindReasoning  StreamChunkKind = "reasoning"
)

// StreamChunk represents one incremental piece of streamed model output.
//
// What:
// `Delta` is the newly arrived text. `Accumulated` is the full text seen so far.
//
// Why:
// Different consumers want different views of the same stream:
// terminals often print only the delta, while stateful UIs usually want the
// full accumulated text. Returning both keeps the boundary flexible.
type StreamChunk struct {
	Kind        StreamChunkKind
	Delta       string
	Accumulated string
}

// ChatModel is the smallest model interface needed by the extracted agent loop.
//
// What:
// The loop sends messages and tool definitions to a model, and receives a Response.
//
// Why:
// We intentionally keep this interface tiny so newcomers can understand the
// control flow first. Later we can add streaming, usage accounting, retries,
// or provider-specific options without changing the core idea.
type ChatModel interface {
	Chat(
		ctx context.Context,
		messages []Message,
		tools []ToolDefinition,
		model string,
		options map[string]any,
	) (*Response, error)
}

// StreamingChatModel is an optional extension for providers that can return
// partial output before the full response is complete.
//
// Why:
// Streaming is important for user experience, but not all providers support it.
// Making it optional keeps the base model interface small while still allowing
// the loop to take advantage of streaming when it is available.
type StreamingChatModel interface {
	ChatModel
	ChatStream(
		ctx context.Context,
		messages []Message,
		tools []ToolDefinition,
		model string,
		options map[string]any,
		onChunk func(StreamChunk),
	) (*Response, error)
}
