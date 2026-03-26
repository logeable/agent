package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
// This tool intentionally constrains reads to RootDir. That keeps the contract
// predictable and prevents the agent from wandering outside the workspace by
// accident.
type ReadFileTool struct {
	// RootDir is the workspace boundary for file access.
	//
	// Why:
	// Agent tools should default to explicit boundaries. If RootDir is empty, we
	// fall back to the current working directory because that is the narrowest
	// sensible default for a local CLI runtime.
	RootDir string

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

func (t ReadFileTool) Execute(_ context.Context, args map[string]any) *tooling.Result {
	relativePath, _ := args["path"].(string)
	if strings.TrimSpace(relativePath) == "" {
		return tooling.Error("read_file requires a non-empty path")
	}

	resolvedPath, err := t.resolvePath(relativePath)
	if err != nil {
		return tooling.Error(err.Error())
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

func (t ReadFileTool) resolvePath(relativePath string) (string, error) {
	rootDir := t.RootDir
	if strings.TrimSpace(rootDir) == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("read_file could not resolve working directory: %w", err)
		}
		rootDir = cwd
	}

	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return "", fmt.Errorf("read_file could not resolve root %q: %w", rootDir, err)
	}

	cleanPath := filepath.Clean(relativePath)
	absPath := filepath.Join(absRoot, cleanPath)
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return "", fmt.Errorf("read_file could not validate path %q: %w", relativePath, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("read_file path %q escapes the workspace root", relativePath)
	}

	return absPath, nil
}
