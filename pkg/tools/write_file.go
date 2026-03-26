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
	// RootDir defines the workspace boundary for writes.
	RootDir string
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

func (t WriteFileTool) Execute(_ context.Context, args map[string]any) *tooling.Result {
	relativePath, _ := args["path"].(string)
	content, _ := args["content"].(string)
	if strings.TrimSpace(relativePath) == "" {
		return tooling.Error("write_file requires a non-empty path")
	}

	resolvedPath, err := t.resolvePath(relativePath)
	if err != nil {
		return tooling.Error(err.Error())
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
	}
}

func (t WriteFileTool) resolvePath(relativePath string) (string, error) {
	rootDir := t.RootDir
	if strings.TrimSpace(rootDir) == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("write_file could not resolve working directory: %w", err)
		}
		rootDir = cwd
	}

	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return "", fmt.Errorf("write_file could not resolve root %q: %w", rootDir, err)
	}

	cleanPath := filepath.Clean(relativePath)
	absPath := filepath.Join(absRoot, cleanPath)
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return "", fmt.Errorf("write_file could not validate path %q: %w", relativePath, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("write_file path %q escapes the workspace root", relativePath)
	}

	return absPath, nil
}
