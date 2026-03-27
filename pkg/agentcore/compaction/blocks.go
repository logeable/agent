package compaction

import "github.com/logeable/agent/pkg/agentcore/provider"

type compactMessageBlock struct {
	messages []provider.Message
}

func buildCompactBlocks(messages []provider.Message) []compactMessageBlock {
	if len(messages) == 0 {
		return nil
	}

	blocks := make([]compactMessageBlock, 0, len(messages))
	for i := 0; i < len(messages); {
		msg := messages[i]
		if len(msg.ToolCalls) == 0 {
			blocks = append(blocks, compactMessageBlock{
				messages: []provider.Message{msg},
			})
			i++
			continue
		}

		callIDs := make(map[string]struct{}, len(msg.ToolCalls))
		for _, call := range msg.ToolCalls {
			if call.ID != "" {
				callIDs[call.ID] = struct{}{}
			}
		}

		block := []provider.Message{msg}
		j := i + 1
		for ; j < len(messages); j++ {
			next := messages[j]
			if next.Role != "tool" {
				break
			}
			if _, ok := callIDs[next.ToolCallID]; !ok {
				break
			}
			block = append(block, next)
		}
		blocks = append(blocks, compactMessageBlock{messages: block})
		i = j
	}

	return blocks
}
