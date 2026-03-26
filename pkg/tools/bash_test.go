package tools

import (
	"context"
	"testing"
	"time"
)

func TestBashToolRequiresApproval(t *testing.T) {
	tool := BashTool{
		WorkDir:         ".",
		RequireApproval: true,
		Timeout:         5 * time.Second,
	}

	result := tool.Execute(context.Background(), map[string]any{
		"command": "pwd",
	})
	if result == nil {
		t.Fatal("Execute() result = nil")
	}
	if result.Approval == nil {
		t.Fatalf("Approval = nil, want non-nil result: %#v", result)
	}
	if result.Approval.Tool != "bash" {
		t.Fatalf("approval tool = %q, want bash", result.Approval.Tool)
	}
	if result.Approval.Reason == "" {
		t.Fatalf("approval reason is empty: %#v", result.Approval)
	}
}
