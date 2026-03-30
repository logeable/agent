package agentacp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/logeable/agent/pkg/agentcore/agent"
	"github.com/logeable/agent/pkg/agentcore/tooling"
	"github.com/logeable/agent/pkg/mcpbridge"
)

type LoopFactory func(workDir string) (*agent.Loop, error)

type Options struct {
	LoopFactory LoopFactory
	Stdin       io.Reader
	Stdout      io.Writer
	AutoApprove bool
}

type Server struct {
	opts       Options
	writeMu    sync.Mutex
	sessionsMu sync.RWMutex
	sessions   map[string]*sessionState
	nextID     atomic.Uint64
}

const defaultACPMCPResponseChars = 8 * 1024

type sessionState struct {
	id      string
	cwd     string
	loop    *agent.Loop
	title   string
	updated time.Time

	mu           sync.Mutex
	activeCancel context.CancelFunc
}

func NewServer(opts Options) *Server {
	if opts.Stdin == nil {
		opts.Stdin = io.Reader(strings.NewReader(""))
	}
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	return &Server{
		opts:     opts,
		sessions: make(map[string]*sessionState),
	}
}

func (s *Server) Serve(ctx context.Context) error {
	if s.opts.LoopFactory == nil {
		return fmt.Errorf("loop factory is nil")
	}

	scanner := bufio.NewScanner(s.opts.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var wg sync.WaitGroup
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var req jsonRPCRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			s.writeResponse(jsonRPCResponse{
				JSONRPC: "2.0",
				Error: &jsonRPCError{
					Code:    -32700,
					Message: "parse error",
				},
			})
			continue
		}

		wg.Add(1)
		go func(req jsonRPCRequest) {
			defer wg.Done()
			s.handleMessage(ctx, req)
		}(req)
	}

	wg.Wait()

	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func (s *Server) handleMessage(ctx context.Context, req jsonRPCRequest) {
	if req.JSONRPC != "" && req.JSONRPC != "2.0" {
		s.writeError(req.ID, -32600, "invalid jsonrpc version")
		return
	}

	switch req.Method {
	case "initialize":
		s.handleInitialize(req)
	case "session/new":
		s.handleSessionNew(req)
	case "session/prompt":
		s.handleSessionPrompt(ctx, req)
	case "session/cancel":
		s.handleSessionCancel(req)
	default:
		if len(req.ID) == 0 {
			return
		}
		s.writeError(req.ID, -32601, "method not found")
	}
}

func (s *Server) handleInitialize(req jsonRPCRequest) {
	var params initializeRequest
	if err := unmarshalParams(req.Params, &params); err != nil {
		s.writeError(req.ID, -32602, err.Error())
		return
	}

	version := protocolVersion
	if params.ProtocolVersion > 0 && params.ProtocolVersion < version {
		version = params.ProtocolVersion
	}

	s.writeResult(req.ID, initializeResponse{
		ProtocolVersion: version,
		AgentCapabilities: agentCapabilities{
			LoadSession: false,
			PromptCapabilities: promptCapabilities{
				Image:           false,
				Audio:           false,
				EmbeddedContext: false,
			},
			MCPCapabilities: mcpCapabilities{
				HTTP: false,
				SSE:  false,
			},
			SessionCapabilities: sessionCapabilities{},
		},
		AgentInfo: implementation{
			Name:    "agent",
			Title:   "agent",
			Version: "0.1.0",
		},
		AuthMethods: []any{},
	})
}

func (s *Server) handleSessionNew(req jsonRPCRequest) {
	var params newSessionRequest
	if err := unmarshalParams(req.Params, &params); err != nil {
		s.writeError(req.ID, -32602, err.Error())
		return
	}
	if strings.TrimSpace(params.Cwd) == "" {
		s.writeError(req.ID, -32602, "cwd is required")
		return
	}

	loop, err := s.opts.LoopFactory(params.Cwd)
	if err != nil {
		s.writeError(req.ID, -32000, err.Error())
		return
	}
	if loop.Events == nil {
		loop.Events = agent.NewEventBus()
	}
	if s.opts.AutoApprove {
		loop.Approval = func(ctx context.Context, req tooling.ApprovalRequest) (bool, error) {
			return true, nil
		}
	}

	manager, err := buildACPMCPManager(context.Background(), params.MCPServers, params.Cwd)
	if err != nil {
		loop.Close()
		s.writeError(req.ID, -32602, err.Error())
		return
	}
	if manager != nil {
		for _, tool := range manager.Tools() {
			loop.Tools.Register(tool)
		}
		loop.AddCloser(manager.Close)
	}

	sessionID := fmt.Sprintf("session-%d", s.nextID.Add(1))
	state := &sessionState{
		id:      sessionID,
		cwd:     params.Cwd,
		loop:    loop,
		title:   sessionID,
		updated: time.Now(),
	}

	s.sessionsMu.Lock()
	s.sessions[sessionID] = state
	s.sessionsMu.Unlock()

	s.writeResult(req.ID, newSessionResponse{SessionID: sessionID})
}

func (s *Server) handleSessionPrompt(parent context.Context, req jsonRPCRequest) {
	var params promptRequest
	if err := unmarshalParams(req.Params, &params); err != nil {
		s.writeError(req.ID, -32602, err.Error())
		return
	}

	state, ok := s.getSession(params.SessionID)
	if !ok {
		s.writeError(req.ID, -32001, "unknown session")
		return
	}

	promptText, err := promptToText(params.Prompt)
	if err != nil {
		s.writeError(req.ID, -32602, err.Error())
		return
	}

	ctx, cancel := context.WithCancel(parent)
	if err := state.beginPrompt(cancel); err != nil {
		cancel()
		s.writeError(req.ID, -32000, err.Error())
		return
	}
	defer state.endPrompt(cancel)

	sub := state.loop.Events.Subscribe(128)

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.forwardEvents(params.SessionID, state, sub.C)
	}()

	_, err = state.loop.Process(ctx, params.SessionID, promptText)
	state.loop.Events.Unsubscribe(sub.ID)
	<-done

	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			s.writeResult(req.ID, promptResponse{StopReason: "cancelled"})
			return
		}
		s.writeError(req.ID, -32000, err.Error())
		return
	}

	state.touch()
	if state.title == state.id {
		state.title = trimTitle(promptText)
	}
	s.emitSessionUpdate(params.SessionID, sessionInfoUpdate{
		SessionUpdate: "session_info_update",
		Title:         stringPtr(state.title),
		UpdatedAt:     state.updated.Format(time.RFC3339),
	})
	s.writeResult(req.ID, promptResponse{StopReason: "end_turn"})
}

func (s *Server) handleSessionCancel(req jsonRPCRequest) {
	var params cancelNotification
	if err := unmarshalParams(req.Params, &params); err != nil {
		if len(req.ID) > 0 {
			s.writeError(req.ID, -32602, err.Error())
		}
		return
	}

	state, ok := s.getSession(params.SessionID)
	if !ok {
		return
	}
	state.cancelPrompt()
	if len(req.ID) > 0 {
		s.writeResult(req.ID, map[string]any{})
	}
}

func (s *Server) forwardEvents(sessionID string, state *sessionState, events <-chan agent.Event) {
	emittedAssistantChunk := false
	for evt := range events {
		switch evt.Kind {
		case agent.EventModelDelta:
			payload, ok := evt.Payload.(agent.ModelDeltaPayload)
			if !ok || strings.TrimSpace(payload.Delta) == "" {
				continue
			}
			emittedAssistantChunk = true
			s.emitSessionUpdate(sessionID, contentChunkUpdate{
				SessionUpdate: "agent_message_chunk",
				Content:       textContent(payload.Delta),
			})
		case agent.EventModelReasoning:
			payload, ok := evt.Payload.(agent.ModelReasoningPayload)
			if !ok || strings.TrimSpace(payload.Delta) == "" {
				continue
			}
			s.emitSessionUpdate(sessionID, contentChunkUpdate{
				SessionUpdate: "agent_thought_chunk",
				Content:       textContent(payload.Delta),
			})
		case agent.EventToolStarted:
			payload, ok := evt.Payload.(agent.ToolStartedPayload)
			if !ok {
				continue
			}
			s.emitSessionUpdate(sessionID, toolCallUpdateNotification{
				SessionUpdate: "tool_call",
				ToolCallID:    evt.Meta.ToolCallID,
				Title:         payload.Tool,
				Kind:          toolKind(payload.Tool),
				Status:        "in_progress",
				RawInput:      payload.Arguments,
			})
		case agent.EventToolFinished:
			payload, ok := evt.Payload.(agent.ToolFinishedPayload)
			if !ok {
				continue
			}
			update := toolCallUpdateNotification{
				SessionUpdate: "tool_call_update",
				ToolCallID:    evt.Meta.ToolCallID,
				Title:         payload.Tool,
				Kind:          toolKind(payload.Tool),
				Status:        "completed",
				RawOutput: map[string]any{
					"forModel":    payload.ForModel,
					"userPreview": payload.UserPreview,
					"errorText":   payload.ErrorText,
					"metadata":    payload.Metadata,
				},
			}
			if payload.IsError {
				update.Status = "failed"
			}
			if text := strings.TrimSpace(firstNonEmpty(payload.UserPreview, payload.ForModel, payload.ErrorText)); text != "" {
				update.Content = []toolCallContent{{
					Type:    "content",
					Content: textContent(text),
				}}
			}
			s.emitSessionUpdate(sessionID, update)
		case agent.EventTurnFinished:
			payload, ok := evt.Payload.(agent.TurnFinishedPayload)
			if ok && !emittedAssistantChunk && strings.TrimSpace(payload.FinalContent) != "" {
				s.emitSessionUpdate(sessionID, contentChunkUpdate{
					SessionUpdate: "agent_message_chunk",
					Content:       textContent(payload.FinalContent),
				})
			}
			state.touch()
			s.emitSessionUpdate(sessionID, sessionInfoUpdate{
				SessionUpdate: "session_info_update",
				Title:         stringPtr(state.title),
				UpdatedAt:     state.updated.Format(time.RFC3339),
			})
		}
	}
}

func (s *Server) emitSessionUpdate(sessionID string, update any) {
	s.writeNotification("session/update", sessionNotification{
		SessionID: sessionID,
		Update:    update,
	})
}

func (s *Server) writeResult(id json.RawMessage, result any) {
	s.writeResponse(jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	})
}

func (s *Server) writeError(id json.RawMessage, code int, message string) {
	s.writeResponse(jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &jsonRPCError{
			Code:    code,
			Message: message,
		},
	})
}

func (s *Server) writeNotification(method string, params any) {
	s.writeJSON(jsonRPCNotification{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	})
}

func (s *Server) writeResponse(resp jsonRPCResponse) {
	s.writeJSON(resp)
}

func (s *Server) writeJSON(value any) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	_, _ = s.opts.Stdout.Write(append(data, '\n'))
}

func (s *Server) getSession(sessionID string) (*sessionState, bool) {
	s.sessionsMu.RLock()
	defer s.sessionsMu.RUnlock()
	state, ok := s.sessions[sessionID]
	return state, ok
}

func (st *sessionState) beginPrompt(cancel context.CancelFunc) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.activeCancel != nil {
		return fmt.Errorf("session prompt already in progress")
	}
	st.activeCancel = cancel
	return nil
}

func (st *sessionState) endPrompt(cancel context.CancelFunc) {
	st.mu.Lock()
	st.activeCancel = nil
	st.mu.Unlock()
	cancel()
}

func (st *sessionState) cancelPrompt() {
	st.mu.Lock()
	cancel := st.activeCancel
	st.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (st *sessionState) touch() {
	st.mu.Lock()
	st.updated = time.Now()
	st.mu.Unlock()
}

func unmarshalParams(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		return fmt.Errorf("missing params")
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("invalid params: %w", err)
	}
	return nil
}

func promptToText(blocks []contentBlock) (string, error) {
	var parts []string
	for _, block := range blocks {
		switch block.Type {
		case "text":
			parts = append(parts, block.Text)
		case "resource_link":
			label := firstNonEmpty(block.Title, block.Name, block.URI)
			parts = append(parts, fmt.Sprintf("[resource] %s", label))
		case "resource":
			if block.Resource == nil {
				return "", fmt.Errorf("resource block missing resource payload")
			}
			if strings.TrimSpace(block.Resource.Resource.Text) != "" {
				parts = append(parts, block.Resource.Resource.Text)
				continue
			}
			parts = append(parts, fmt.Sprintf("[resource] %s", block.Resource.Resource.URI))
		default:
			return "", fmt.Errorf("unsupported prompt content type %q", block.Type)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n")), nil
}

func textContent(text string) content {
	return content{
		Content: contentBlock{
			Type: "text",
			Text: text,
		},
	}
}

func toolKind(name string) string {
	switch strings.TrimSpace(strings.ToLower(name)) {
	case "read_file":
		return "read"
	case "write_file", "edit_file":
		return "edit"
	case "bash":
		return "execute"
	case "web_fetch":
		return "fetch"
	default:
		return "other"
	}
}

func trimTitle(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return ""
	}
	const max = 80
	if len(prompt) <= max {
		return prompt
	}
	return strings.TrimSpace(prompt[:max]) + "..."
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func stringPtr(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

func buildACPMCPManager(ctx context.Context, servers []acpMCPServer, workDir string) (*mcpbridge.Manager, error) {
	if len(servers) == 0 {
		return nil, nil
	}

	cfg := mcpbridge.Config{
		Servers: make(map[string]mcpbridge.ServerConfig, len(servers)),
	}
	for _, server := range servers {
		switch server.Type {
		case "stdio":
			cfg.Servers[server.Name] = mcpbridge.ServerConfig{
				Command: server.Command,
				Args:    append([]string(nil), server.Args...),
				Env:     cloneStringMap(server.Env),
			}
		case "http", "sse":
			return nil, fmt.Errorf("unsupported ACP MCP transport %q", server.Type)
		default:
			return nil, fmt.Errorf("unsupported ACP MCP transport %q", server.Type)
		}
	}

	manager := mcpbridge.NewManager(defaultACPMCPResponseChars)
	if err := manager.LoadStdioServers(ctx, cfg, workDir); err != nil {
		return nil, err
	}
	return manager, nil
}

func cloneStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]string, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}
