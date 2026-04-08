package agentclitui

import (
	"testing"

	"github.com/charmbracelet/bubbles/viewport"
)

func TestRebuildTranscriptRespectsManualScrollPosition(t *testing.T) {
	m := model{
		messageView: viewport.New(20, 3),
		autoFollow:  false,
		messages: []messageBlock{
			{role: roleAssistant, title: "Assistant", content: "one"},
			{role: roleAssistant, title: "Assistant", content: "two"},
			{role: roleAssistant, title: "Assistant", content: "three"},
			{role: roleAssistant, title: "Assistant", content: "four"},
			{role: roleAssistant, title: "Assistant", content: "five"},
		},
	}
	m.messageView.SetContent(m.renderTranscript())
	m.messageView.GotoBottom()
	m.messageView.LineUp(2)
	offset := m.messageView.YOffset

	m.messages = append(m.messages, messageBlock{role: roleAssistant, title: "Assistant", content: "six"})
	m.rebuildTranscript()

	if m.messageView.YOffset != offset {
		t.Fatalf("YOffset = %d, want %d after rebuild without auto-follow", m.messageView.YOffset, offset)
	}
}

func TestRebuildTranscriptFollowsWhenEnabled(t *testing.T) {
	m := model{
		messageView: viewport.New(20, 3),
		autoFollow:  true,
		messages: []messageBlock{
			{role: roleAssistant, title: "Assistant", content: "one"},
			{role: roleAssistant, title: "Assistant", content: "two"},
			{role: roleAssistant, title: "Assistant", content: "three"},
			{role: roleAssistant, title: "Assistant", content: "four"},
		},
	}
	m.messageView.SetContent(m.renderTranscript())
	m.rebuildTranscript()
	before := m.messageView.YOffset
	m.messages = append(m.messages, messageBlock{role: roleAssistant, title: "Assistant", content: "five"})
	m.rebuildTranscript()

	if m.messageView.YOffset < before {
		t.Fatalf("YOffset = %d, want follow-to-bottom >= %d", m.messageView.YOffset, before)
	}
	if !m.messageView.AtBottom() {
		t.Fatal("viewport should remain at bottom when auto-follow is enabled")
	}
}
