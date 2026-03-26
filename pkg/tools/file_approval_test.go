package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestReadFileToolRequiresApproval(t *testing.T) {
	root := t.TempDir()
	outsideRoot := t.TempDir()
	path := filepath.Join(outsideRoot, "note.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	tool := ReadFileTool{
		PathPolicy: PathPolicy{Scope: PathScopeWorkspace, Roots: []string{root}},
	}
	result := tool.Execute(context.Background(), map[string]any{"path": path})
	if result == nil || result.Approval == nil {
		t.Fatalf("result = %#v, want approval request", result)
	}
}

func TestEditFileToolRequiresApproval(t *testing.T) {
	root := t.TempDir()
	outsideRoot := t.TempDir()
	path := filepath.Join(outsideRoot, "note.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	tool := EditFileTool{
		PathPolicy: PathPolicy{Scope: PathScopeWorkspace, Roots: []string{root}},
	}
	result := tool.Execute(context.Background(), map[string]any{
		"path": path, "old_string": "hello", "new_string": "world",
	})
	if result == nil || result.Approval == nil {
		t.Fatalf("result = %#v, want approval request", result)
	}
}

func TestWriteFileToolRequiresApproval(t *testing.T) {
	root := t.TempDir()
	outsideRoot := t.TempDir()
	path := filepath.Join(outsideRoot, "note.txt")

	tool := WriteFileTool{
		PathPolicy: PathPolicy{Scope: PathScopeWorkspace, Roots: []string{root}},
	}
	result := tool.Execute(context.Background(), map[string]any{
		"path": path, "content": "hello",
	})
	if result == nil || result.Approval == nil {
		t.Fatalf("result = %#v, want approval request", result)
	}
}

func TestReadFileToolInsideRootDoesNotRequireApproval(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "note.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	tool := ReadFileTool{
		PathPolicy: PathPolicy{Scope: PathScopeWorkspace, Roots: []string{root}},
	}
	result := tool.Execute(context.Background(), map[string]any{"path": path})
	if result == nil {
		t.Fatal("result = nil")
	}
	if result.Approval != nil {
		t.Fatalf("approval = %#v, want nil", result.Approval)
	}
	if result.IsError {
		t.Fatalf("result = %#v, want success", result)
	}
}
