package agentacp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/logeable/agent/pkg/agentcore/agent"
	"github.com/logeable/agent/pkg/agentcore/provider"
	"github.com/logeable/agent/pkg/agentcore/session"
	"github.com/logeable/agent/pkg/agentcore/tooling"
)

func TestServerInitializeNewAndPrompt(t *testing.T) {
	_, inWriter, outReader, done := newTestServer(t, func(workDir string) (*agent.Loop, error) {
		loop := &agent.Loop{
			Model: &streamingTestModel{
				response: &provider.Response{Content: "hello from acp"},
				chunks: []provider.StreamChunk{
					{Kind: provider.StreamChunkKindOutputText, Delta: "hello ", Accumulated: "hello "},
					{Kind: provider.StreamChunkKindOutputText, Delta: "from acp", Accumulated: "hello from acp"},
				},
			},
			ModelName: "test",
			Tools:     tooling.NewRegistry(),
			Sessions:  session.NewMemoryStore(),
			Context: agent.ContextBuilder{
				SystemPrompt: "system",
			},
			Events: agent.NewEventBus(),
		}
		return loop, nil
	})
	defer func() {
		_ = inWriter.Close()
		<-done
	}()

	writeRequest(t, inWriter, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": 1,
		},
	})
	resp := readMessage(t, outReader)
	if got := lookupNumber(t, resp, "id"); got != 1 {
		t.Fatalf("initialize id = %v, want 1", got)
	}
	if got := lookupNumber(t, lookupMap(t, resp, "result"), "protocolVersion"); got != 1 {
		t.Fatalf("protocolVersion = %v, want 1", got)
	}

	writeRequest(t, inWriter, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "session/new",
		"params": map[string]any{
			"cwd":        t.TempDir(),
			"mcpServers": []any{},
		},
	})
	resp = readMessage(t, outReader)
	sessionID := lookupString(t, lookupMap(t, resp, "result"), "sessionId")
	if sessionID == "" {
		t.Fatal("sessionId is empty")
	}

	writeRequest(t, inWriter, map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "session/prompt",
		"params": map[string]any{
			"sessionId": sessionID,
			"prompt": []any{
				map[string]any{
					"type": "text",
					"text": "say hi",
				},
			},
		},
	})

	var sawChunk bool
	for {
		msg := readMessage(t, outReader)
		if method, _ := msg["method"].(string); method == "session/update" {
			params := lookupMap(t, msg, "params")
			update := lookupMap(t, params, "update")
			if lookupString(t, update, "sessionUpdate") == "agent_message_chunk" {
				content := lookupMap(t, lookupMap(t, update, "content"), "content")
				if got := lookupString(t, content, "text"); got == "hello " || got == "from acp" {
					sawChunk = true
				}
			}
			continue
		}
		if got := lookupNumber(t, msg, "id"); got == 3 {
			result := lookupMap(t, msg, "result")
			if stop := lookupString(t, result, "stopReason"); stop != "end_turn" {
				t.Fatalf("stopReason = %q, want end_turn", stop)
			}
			break
		}
	}
	if !sawChunk {
		t.Fatal("did not observe agent_message_chunk update")
	}
}

func TestServerCancelsPrompt(t *testing.T) {
	_, inWriter, outReader, done := newTestServer(t, func(workDir string) (*agent.Loop, error) {
		loop := &agent.Loop{
			Model:     &blockingTestModel{},
			ModelName: "test",
			Tools:     tooling.NewRegistry(),
			Sessions:  session.NewMemoryStore(),
			Context: agent.ContextBuilder{
				SystemPrompt: "system",
			},
			Events: agent.NewEventBus(),
		}
		return loop, nil
	})
	defer func() {
		_ = inWriter.Close()
		<-done
	}()
	writeRequest(t, inWriter, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "session/new",
		"params": map[string]any{
			"cwd":        t.TempDir(),
			"mcpServers": []any{},
		},
	})
	resp := readMessage(t, outReader)
	sessionID := lookupString(t, lookupMap(t, resp, "result"), "sessionId")

	writeRequest(t, inWriter, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "session/prompt",
		"params": map[string]any{
			"sessionId": sessionID,
			"prompt": []any{
				map[string]any{"type": "text", "text": "block"},
			},
		},
	})

	time.Sleep(50 * time.Millisecond)
	writeRequest(t, inWriter, map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/cancel",
		"params": map[string]any{
			"sessionId": sessionID,
		},
	})

	for {
		msg := readMessage(t, outReader)
		if method, _ := msg["method"].(string); method == "session/update" {
			continue
		}
		if got := lookupNumber(t, msg, "id"); got == 2 {
			result := lookupMap(t, msg, "result")
			if stop := lookupString(t, result, "stopReason"); stop != "cancelled" {
				t.Fatalf("stopReason = %q, want cancelled", stop)
			}
			return
		}
	}
}

func newTestServer(t *testing.T, factory LoopFactory) (*Server, *io.PipeWriter, *bufio.Reader, <-chan error) {
	t.Helper()

	inReader, inWriter := io.Pipe()
	outReader, outWriter := io.Pipe()
	server := NewServer(Options{
		LoopFactory: factory,
		Stdin:       inReader,
		Stdout:      outWriter,
	})

	done := make(chan error, 1)
	go func() {
		done <- server.Serve(context.Background())
		_ = outWriter.Close()
	}()

	return server, inWriter, bufio.NewReader(outReader), done
}

func writeRequest(t *testing.T, w io.Writer, request map[string]any) {
	t.Helper()
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if _, err := w.Write(append(data, '\n')); err != nil {
		t.Fatalf("write request: %v", err)
	}
}

func readMessage(t *testing.T, r *bufio.Reader) map[string]any {
	t.Helper()
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := r.ReadString('\n')
		ch <- result{line: line, err: err}
	}()

	select {
	case res := <-ch:
		if res.err != nil {
			t.Fatalf("read message: %v", res.err)
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(res.line)), &out); err != nil {
			t.Fatalf("unmarshal message %q: %v", res.line, err)
		}
		return out
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for ACP message")
		return nil
	}
}

func lookupMap(t *testing.T, m map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := m[key].(map[string]any)
	if !ok {
		t.Fatalf("%q = %#v, want object", key, m[key])
	}
	return value
}

func lookupString(t *testing.T, m map[string]any, key string) string {
	t.Helper()
	value, ok := m[key].(string)
	if !ok {
		t.Fatalf("%q = %#v, want string", key, m[key])
	}
	return value
}

func lookupNumber(t *testing.T, m map[string]any, key string) int {
	t.Helper()
	value, ok := m[key].(float64)
	if !ok {
		t.Fatalf("%q = %#v, want number", key, m[key])
	}
	return int(value)
}

type streamingTestModel struct {
	response *provider.Response
	chunks   []provider.StreamChunk
}

func (m *streamingTestModel) Chat(
	ctx context.Context,
	messages []provider.Message,
	tools []provider.ToolDefinition,
	model string,
	options map[string]any,
) (*provider.Response, error) {
	return m.response, nil
}

func (m *streamingTestModel) ChatStream(
	ctx context.Context,
	messages []provider.Message,
	tools []provider.ToolDefinition,
	model string,
	options map[string]any,
	onChunk func(provider.StreamChunk),
) (*provider.Response, error) {
	for _, chunk := range m.chunks {
		onChunk(chunk)
	}
	return m.response, nil
}

type blockingTestModel struct{}

func (m *blockingTestModel) Chat(
	ctx context.Context,
	messages []provider.Message,
	tools []provider.ToolDefinition,
	model string,
	options map[string]any,
) (*provider.Response, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
