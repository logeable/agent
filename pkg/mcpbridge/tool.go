package mcpbridge

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/logeable/agent/pkg/agentcore/tooling"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tool adapts an MCP tool into the local runtime Tool interface.
type Tool struct {
	manager    *Manager
	serverName string
	tool       *mcp.Tool
}

func NewTool(manager *Manager, serverName string, tool *mcp.Tool) tooling.Tool {
	return &Tool{
		manager:    manager,
		serverName: serverName,
		tool:       tool,
	}
}

func (t *Tool) Name() string {
	server := sanitizeIdentifier(t.serverName)
	name := sanitizeIdentifier(t.tool.Name)
	full := "mcp_" + server + "_" + name
	if len(full) <= 64 {
		return full
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(t.serverName + "\x00" + t.tool.Name))
	suffix := fmt.Sprintf("%08x", h.Sum32())
	base := full[:64-len(suffix)-1]
	base = strings.TrimRight(base, "_")
	return base + "_" + suffix
}

func (t *Tool) Description() string {
	if strings.TrimSpace(t.tool.Description) == "" {
		return fmt.Sprintf("MCP tool from %s", t.serverName)
	}
	return fmt.Sprintf("[MCP:%s] %s", t.serverName, t.tool.Description)
}

func (t *Tool) Parameters() map[string]any {
	return schemaAsMap(t.tool.InputSchema)
}

func (t *Tool) Execute(ctx context.Context, args map[string]any) *tooling.Result {
	result, err := t.manager.CallTool(ctx, t.serverName, t.tool.Name, args)
	if err != nil {
		return tooling.Error(fmt.Sprintf("mcp tool failed: %v", err))
	}
	text := resultText(result.Content)
	if result.IsError {
		return tooling.Error(text)
	}
	if text == "" {
		text = "{}"
	}
	return &tooling.Result{
		ForModel: text,
		ForUser:  fmt.Sprintf("Ran MCP tool %s", t.Name()),
		Metadata: map[string]any{
			"server":    t.serverName,
			"tool":      t.tool.Name,
			"mcp_tool":  true,
			"is_error":  result.IsError,
			"content_n": len(result.Content),
		},
	}
}

func sanitizeIdentifier(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "unnamed"
	}
	var b strings.Builder
	prevUnderscore := false
	for _, r := range value {
		allowed := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-'
		if !allowed {
			if !prevUnderscore {
				b.WriteByte('_')
				prevUnderscore = true
			}
			continue
		}
		if r == '_' && prevUnderscore {
			continue
		}
		prevUnderscore = r == '_'
		b.WriteRune(r)
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "unnamed"
	}
	return out
}
