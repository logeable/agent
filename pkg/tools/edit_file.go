package tools

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/logeable/agent/pkg/agentcore/tooling"
)

// EditFileTool performs a constrained in-place text replacement.
//
// What:
// The caller identifies an existing file, an old string to match, and a new
// string to insert.
//
// Why:
// Real agent editing is usually incremental, not full-file rewrite. At the
// same time, a stable execution core benefits from a very explicit edit
// contract. This first version therefore uses exact string replacement instead
// of a richer patch DSL.
//
// The rule is intentionally strict:
// - the file must already exist
// - old_string must be non-empty
// - by default exactly one match must exist
// - replace_all can be set when multiple matches are expected
//
// Those constraints make failures easier for both the model and the user to
// understand.
type EditFileTool struct {
	// PathPolicy controls which filesystem paths this tool may access.
	PathPolicy PathPolicy
}

func (t EditFileTool) Name() string { return "edit_file" }

func (t EditFileTool) Description() string {
	return "Replace text inside an existing workspace file."
}

func (t EditFileTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Relative path to the existing file inside the workspace root.",
			},
			"old_string": map[string]any{
				"type":        "string",
				"description": "Exact text that must be found in the file.",
			},
			"new_string": map[string]any{
				"type":        "string",
				"description": "Replacement text.",
			},
			"replace_all": map[string]any{
				"type":        "boolean",
				"description": "Replace every match instead of requiring exactly one match.",
			},
		},
		"required": []string{"path", "old_string", "new_string"},
	}
}

func (t EditFileTool) Execute(ctx context.Context, args map[string]any) *tooling.Result {
	relativePath, _ := args["path"].(string)
	oldString, _ := args["old_string"].(string)
	newString, _ := args["new_string"].(string)
	replaceAll, _ := args["replace_all"].(bool)

	if strings.TrimSpace(relativePath) == "" {
		return tooling.Error("edit_file requires a non-empty path")
	}
	if oldString == "" {
		return tooling.Error("edit_file requires old_string to be non-empty")
	}

	resolvedPath, escaped, err := t.PathPolicy.ResolvePathWithEscape(relativePath)
	if err != nil {
		return tooling.Error(err.Error())
	}
	if escaped {
		if !tooling.ToolApproved(ctx, t.Name()) {
			return tooling.RequiresApproval(tooling.ApprovalRequest{
				Tool:        t.Name(),
				Reason:      "file edit escapes the configured roots and requires approval",
				ActionLabel: "edit file outside configured roots",
				Details: map[string]any{
					"path":          relativePath,
					"resolved_path": resolvedPath,
				},
			})
		}
	}

	info, err := os.Stat(resolvedPath)
	if err != nil {
		return tooling.Error(fmt.Sprintf("edit_file could not stat %s: %v", relativePath, err))
	}
	if info.IsDir() {
		return tooling.Error(fmt.Sprintf("edit_file target %s is a directory", relativePath))
	}

	data, err := os.ReadFile(resolvedPath)
	if err != nil {
		return tooling.Error(fmt.Sprintf("edit_file failed to read %s: %v", relativePath, err))
	}

	content := string(data)
	matchCount := strings.Count(content, oldString)
	if matchCount == 0 {
		return tooling.Error(fmt.Sprintf("edit_file could not find old_string in %s", relativePath))
	}
	if !replaceAll && matchCount != 1 {
		return tooling.Error(fmt.Sprintf("edit_file found %d matches in %s; set replace_all=true to replace all matches", matchCount, relativePath))
	}

	var updated string
	replacements := 1
	if replaceAll {
		updated = strings.ReplaceAll(content, oldString, newString)
		replacements = matchCount
	} else {
		updated = strings.Replace(content, oldString, newString, 1)
	}

	if err := os.WriteFile(resolvedPath, []byte(updated), info.Mode().Perm()); err != nil {
		return tooling.Error(fmt.Sprintf("edit_file failed to write %s: %v", relativePath, err))
	}

	return &tooling.Result{
		ForModel: fmt.Sprintf("edited file %s; replacements=%d", relativePath, replacements),
		ForUser:  fmt.Sprintf("Edited %s", relativePath),
	}
}
