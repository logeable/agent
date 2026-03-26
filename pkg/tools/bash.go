package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/logeable/agent/pkg/agentcore/tooling"
)

// BashTool runs a shell command inside a configured working directory.
//
// What:
// The caller provides a command string and the tool executes it through a shell.
//
// Why:
// A coding agent often needs one escape hatch into the surrounding environment
// for search, build, test, and inspection tasks. This tool provides that
// escape hatch while still keeping a small, explicit contract around execution
// boundaries.
//
// The first version intentionally keeps the policy simple:
// - commands run inside WorkDir
// - a timeout is always applied
// - stdout and stderr are combined
// - output is truncated to MaxOutputBytes
// - interactive input is not supported
type BashTool struct {
	// WorkDir is the directory where commands run.
	WorkDir string

	// Timeout bounds command execution time.
	//
	// Why:
	// Shell commands are powerful but dangerous to leave unbounded. A timeout is
	// part of the tool contract, not an optional afterthought.
	Timeout time.Duration

	// Shell is the executable used to interpret the command string.
	//
	// If empty, the tool uses /bin/sh.
	Shell string

	// MaxOutputBytes limits how much combined output is returned.
	MaxOutputBytes int
}

func (t BashTool) Name() string { return "bash" }

func (t BashTool) Description() string {
	return "Run a shell command in the workspace and return combined stdout/stderr."
}

func (t BashTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "Shell command to execute.",
			},
		},
		"required": []string{"command"},
	}
}

func (t BashTool) Execute(ctx context.Context, args map[string]any) *tooling.Result {
	command, _ := args["command"].(string)
	if strings.TrimSpace(command) == "" {
		return tooling.Error("bash requires a non-empty command")
	}

	workDir := t.WorkDir
	if strings.TrimSpace(workDir) == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return tooling.Error(fmt.Sprintf("bash could not resolve working directory: %v", err))
		}
		workDir = cwd
	}

	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		return tooling.Error(fmt.Sprintf("bash could not resolve workdir %q: %v", workDir, err))
	}

	timeout := t.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	shell := t.Shell
	if strings.TrimSpace(shell) == "" {
		shell = "/bin/sh"
	}

	cmd := exec.CommandContext(runCtx, shell, "-lc", command)
	cmd.Dir = absWorkDir

	limit := t.MaxOutputBytes
	if limit <= 0 {
		limit = 64 * 1024
	}

	outputBuffer := limitedOutputBuffer{limit: limit}
	cmd.Stdout = &outputBuffer
	cmd.Stderr = &outputBuffer

	err = cmd.Run()
	output := outputBuffer.String()
	if outputBuffer.truncated {
		output += fmt.Sprintf("\n[output truncated to %d bytes]", limit)
	}

	modelText := fmt.Sprintf("command: %s\nworkdir: %s\noutput:\n%s", command, absWorkDir, output)
	userText := fmt.Sprintf("Ran bash command in %s", absWorkDir)

	if runCtx.Err() == context.DeadlineExceeded {
		return &tooling.Result{
			ForModel: modelText + "\nstatus: timed out",
			ForUser:  userText + " (timed out)",
			IsError:  true,
			Err:      runCtx.Err(),
		}
	}

	if err != nil {
		return &tooling.Result{
			ForModel: modelText + fmt.Sprintf("\nstatus: failed: %v", err),
			ForUser:  userText + " (failed)",
			IsError:  true,
			Err:      err,
		}
	}

	return &tooling.Result{
		ForModel: modelText + "\nstatus: ok",
		ForUser:  userText,
	}
}

// limitedOutputBuffer stores at most limit bytes while still recording that
// more output existed.
//
// Why:
// The shell tool needs bounded output for model context stability, but the
// caller also needs to know when truncation happened.
type limitedOutputBuffer struct {
	data      []byte
	limit     int
	truncated bool
}

func (b *limitedOutputBuffer) Write(p []byte) (int, error) {
	if len(b.data) < b.limit {
		remaining := b.limit - len(b.data)
		if len(p) > remaining {
			b.data = append(b.data, p[:remaining]...)
			b.truncated = true
		} else {
			b.data = append(b.data, p...)
		}
	} else if len(p) > 0 {
		b.truncated = true
	}
	return len(p), nil
}

func (b *limitedOutputBuffer) String() string {
	return string(b.data)
}
