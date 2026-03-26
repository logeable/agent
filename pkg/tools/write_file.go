package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/logeable/agent/pkg/agentcore/tooling"
)

// WriteFileTool creates or fully replaces a file inside a configured root.
//
// What:
// The caller provides a path and the complete desired contents of the file.
//
// Why:
// Agents need one tool whose semantics are simple and explicit: "make this file
// exactly equal to this content". That contract is much easier to reason about
// than incremental patching when the agent is creating a new file or replacing
// a generated artifact.
type WriteFileTool struct {
	// PathPolicy controls which filesystem paths this tool may access.
	PathPolicy PathPolicy
}

func (t WriteFileTool) Name() string { return "write_file" }

func (t WriteFileTool) Description() string {
	return "Create or replace a text file inside the workspace."
}

func (t WriteFileTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Relative path to the file inside the workspace root.",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "Complete file contents to write.",
			},
		},
		"required": []string{"path", "content"},
	}
}

func (t WriteFileTool) Execute(ctx context.Context, args map[string]any) *tooling.Result {
	relativePath, _ := args["path"].(string)
	content, _ := args["content"].(string)
	if strings.TrimSpace(relativePath) == "" {
		return tooling.Error("write_file requires a non-empty path")
	}

	resolvedPath, escaped, err := t.PathPolicy.ResolvePathWithEscape(relativePath)
	if err != nil {
		return tooling.Error(err.Error())
	}
	if escaped {
		if !tooling.ToolApproved(ctx, t.Name()) {
			return tooling.RequiresApproval(tooling.ApprovalRequest{
				Tool:        t.Name(),
				Reason:      "file write escapes the configured roots and requires approval",
				ActionLabel: "write file outside configured roots",
				Details: map[string]any{
					"path":          relativePath,
					"resolved_path": resolvedPath,
				},
			})
		}
	}

	if err := os.MkdirAll(filepath.Dir(resolvedPath), 0o755); err != nil {
		return tooling.Error(fmt.Sprintf("write_file could not create parent directories for %s: %v", relativePath, err))
	}

	if err := os.WriteFile(resolvedPath, []byte(content), 0o644); err != nil {
		return tooling.Error(fmt.Sprintf("write_file failed for %s: %v", relativePath, err))
	}

	return &tooling.Result{
		ForModel: fmt.Sprintf("wrote file %s (%d bytes)", relativePath, len(content)),
		ForUser:  fmt.Sprintf("Wrote %s", relativePath),
		Metadata: map[string]any{
			"path":  relativePath,
			"bytes": len(content),
		},
	}
}
