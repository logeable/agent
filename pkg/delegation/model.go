package delegation

import (
	"context"

	"github.com/logeable/agent/pkg/agentcore/provider"
)

// ToolCallLimiterModel caps delegate_task fan-out within one model response.
type ToolCallLimiterModel struct {
	Inner       provider.ChatModel
	MaxChildren int
}

func (m ToolCallLimiterModel) Chat(
	ctx context.Context,
	messages []provider.Message,
	tools []provider.ToolDefinition,
	model string,
	options map[string]any,
) (*provider.Response, error) {
	response, err := m.Inner.Chat(ctx, messages, tools, model, options)
	if err != nil || response == nil {
		return response, err
	}
	response.ToolCalls = TruncateDelegationCalls(response.ToolCalls, m.MaxChildren)
	return response, nil
}

func (m ToolCallLimiterModel) ChatStream(
	ctx context.Context,
	messages []provider.Message,
	tools []provider.ToolDefinition,
	model string,
	options map[string]any,
	onChunk func(provider.StreamChunk),
) (*provider.Response, error) {
	streaming, ok := m.Inner.(provider.StreamingChatModel)
	if !ok {
		return m.Chat(ctx, messages, tools, model, options)
	}
	response, err := streaming.ChatStream(ctx, messages, tools, model, options, onChunk)
	if err != nil || response == nil {
		return response, err
	}
	response.ToolCalls = TruncateDelegationCalls(response.ToolCalls, m.MaxChildren)
	return response, nil
}
