package tools

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/logeable/agent/pkg/agentcore/tooling"
)

// ReadFileTool reads a file from a configured root directory.
//
// What:
// The caller supplies a relative path and the tool returns the file contents.
//
// Why:
// Reading files is one of the most fundamental agent capabilities. An agent
// cannot reason reliably about source code or configuration unless it can read
// the current contents directly from disk.
//
// File access is delegated to PathPolicy so read/edit/write all share the same
// boundary semantics.
type ReadFileTool struct {
	// PathPolicy controls which filesystem paths this tool may access.
	PathPolicy PathPolicy

	// MaxBytes limits how much content is returned to the model.
	//
	// Why:
	// A file tool without output limits can easily overwhelm context windows.
	// The first version keeps the rule simple: return at most MaxBytes bytes and
	// clearly mark when truncation happened.
	MaxBytes int64
}

func (t ReadFileTool) Name() string { return "read_file" }

func (t ReadFileTool) Description() string {
	return "Read a UTF-8 text file from the workspace."
}

func (t ReadFileTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Relative path to the file inside the workspace root.",
			},
		},
		"required": []string{"path"},
	}
}

func (t ReadFileTool) Execute(ctx context.Context, args map[string]any) *tooling.Result {
	relativePath, _ := args["path"].(string)
	if strings.TrimSpace(relativePath) == "" {
		return tooling.Error("read_file requires a non-empty path")
	}

	resolvedPath, escaped, err := t.PathPolicy.ResolvePathWithEscape(relativePath)
	if err != nil {
		return tooling.Error(err.Error())
	}
	if escaped {
		if !tooling.ToolApproved(ctx, t.Name()) {
			return tooling.RequiresApproval(tooling.ApprovalRequest{
				Tool:        t.Name(),
				Reason:      "file read escapes the configured roots and requires approval",
				ActionLabel: "read file outside configured roots",
				Details: map[string]any{
					"path":          relativePath,
					"resolved_path": resolvedPath,
				},
			})
		}
	}

	data, err := os.ReadFile(resolvedPath)
	if err != nil {
		return tooling.Error(fmt.Sprintf("read_file failed for %s: %v", relativePath, err))
	}

	limit := t.MaxBytes
	if limit <= 0 {
		limit = 128 * 1024
	}

	truncated := int64(len(data)) > limit
	if truncated {
		data = data[:limit]
	}

	modelText := fmt.Sprintf("path: %s\nbytes: %d", relativePath, len(data))
	if truncated {
		modelText += fmt.Sprintf(" (truncated to %d bytes)", limit)
	}
	modelText += "\ncontent:\n" + string(data)

	userText := fmt.Sprintf("Read %s", relativePath)
	if truncated {
		userText += fmt.Sprintf(" (truncated to %d bytes)", limit)
	}

	return &tooling.Result{
		ForModel: modelText,
		ForUser:  userText,
	}
}
