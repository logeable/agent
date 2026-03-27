package mcpbridge

import (
	"strings"
	"testing"
)

func TestSummarizeForModelKeepsSmallText(t *testing.T) {
	text := "hello world"

	got, truncated := summarizeForModel(text, 8*1024)
	if truncated {
		t.Fatal("truncated = true, want false")
	}
	if got != text {
		t.Fatalf("summarized text = %q, want %q", got, text)
	}
}

func TestSummarizeForModelTruncatesLargeText(t *testing.T) {
	const limit = 8 * 1024
	text := strings.Repeat("a", limit+100)

	got, truncated := summarizeForModel(text, limit)
	if !truncated {
		t.Fatal("truncated = false, want true")
	}
	if !strings.Contains(got, "MCP tool output truncated for model context.") {
		t.Fatalf("summary = %q, want truncation header", got)
	}
	if !strings.Contains(got, "Original length: 8292 characters.") {
		t.Fatalf("summary = %q, want original length detail", got)
	}
	if len(got) <= limit {
		t.Fatalf("summary length = %d, want header + truncated body", len(got))
	}
}
