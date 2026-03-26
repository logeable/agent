package demo

import (
	"context"
	"fmt"
	"time"

	"github.com/logeable/agent/pkg/agentcore/tools"
)

// EchoTool is the smallest possible example tool.
//
// Why:
// It keeps the tool contract easy to understand before introducing more
// realistic tools such as files, shell, or network calls.
type EchoTool struct{}

func (EchoTool) Name() string { return "echo" }

func (EchoTool) Description() string {
	return "Return the provided text unchanged."
}

func (EchoTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"text": map[string]any{
				"type":        "string",
				"description": "The text to echo back.",
			},
		},
	}
}

func (EchoTool) Execute(_ context.Context, args map[string]any) *tools.Result {
	text, _ := args["text"].(string)
	return &tools.Result{
		ForModel: fmt.Sprintf("echo result: %s", text),
		ForUser:  text,
	}
}

// TimeTool shows a tool whose output depends on the outside world.
//
// Why:
// This is the simplest example of "the model cannot know this reliably by
// itself, so it should use a tool".
type TimeTool struct{}

func (TimeTool) Name() string { return "get_time" }

func (TimeTool) Description() string {
	return "Return the current local time."
}

func (TimeTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"timezone": map[string]any{
				"type":        "string",
				"description": "Optional timezone label for display purposes.",
			},
		},
	}
}

func (TimeTool) Execute(_ context.Context, args map[string]any) *tools.Result {
	timezone, _ := args["timezone"].(string)
	now := time.Now()
	return &tools.Result{
		ForModel: fmt.Sprintf("current time (%s): %s", timezone, now.Format(time.RFC3339)),
		ForUser:  now.Format(time.RFC1123),
	}
}
