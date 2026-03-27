package agentclitui

import (
	"context"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/logeable/agent/internal/agentclirun"
	"github.com/logeable/agent/pkg/agentcore/agent"
	"github.com/logeable/agent/pkg/agentcore/tooling"
)

// Options controls the interactive TUI behavior without leaking TUI details
// into cmd/agentcli.
type Options struct {
	SessionKey  string
	ProfileName string
	Stream      bool
	ShowEvents  bool
	AutoApprove bool
}

type processDoneMsg struct {
	response string
	err      error
}

type runtimeEventMsg struct {
	event agent.Event
}

type approvalPromptMsg struct {
	request tooling.ApprovalRequest
	reply   chan bool
}

type messageRole string

const (
	roleSystem    messageRole = "system"
	roleUser      messageRole = "user"
	roleAssistant messageRole = "assistant"
	roleReasoning messageRole = "reasoning"
	roleError     messageRole = "error"
)

type messageBlock struct {
	role    messageRole
	title   string
	content string
}

type model struct {
	loop       *agent.Loop
	profileName string
	sessionKey string
	stream     bool

	input          textarea.Model
	messageView    viewport.Model
	width          int
	height         int
	headerHeight   int
	footerHeight   int
	approvalHeight int

	status        string
	activity      string
	busy          bool
	streamingSeen bool

	awaitingApproval bool
	approvalRequest  tooling.ApprovalRequest
	approvalReply    chan bool

	messages           []messageBlock
	activeAssistantIdx int
	activeReasoningIdx int
	activeToolIdx      int
	reasoningExpanded  bool
}

// Run starts the interactive TUI for agentcli. The CLI entrypoint stays small
// while this package owns the Bubble Tea specific state machine.
func Run(loop *agent.Loop, opts Options) error {
	m := newModel(loop, opts)
	program := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())

	loop.Approval = func(ctx context.Context, req tooling.ApprovalRequest) (bool, error) {
		if opts.AutoApprove {
			program.Send(runtimeEventMsg{
				event: agent.Event{
					Kind: agent.EventApprovalResolved,
					Payload: agent.ApprovalResolvedPayload{
						Tool:      req.Tool,
						RequestID: req.ID,
						Approved:  true,
						Reason:    "auto-approved by CLI flag",
					},
				},
			})
			return true, nil
		}

		reply := make(chan bool, 1)
		program.Send(approvalPromptMsg{request: req, reply: reply})
		select {
		case approved, ok := <-reply:
			if !ok {
				return false, context.Canceled
			}
			return approved, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}

	sub := loop.Events.Subscribe(128)
	defer loop.Events.Unsubscribe(sub.ID)
	go func() {
		for evt := range sub.C {
			program.Send(runtimeEventMsg{event: evt})
		}
	}()

	_, err := program.Run()
	return err
}

func newModel(loop *agent.Loop, opts Options) model {
	input := textarea.New()
	input.Placeholder = ""
	input.Focus()
	input.CharLimit = 0
	input.Prompt = ""
	input.ShowLineNumbers = false
	input.SetHeight(3)
	bg := lipgloss.Color("236")
	fg := lipgloss.Color("252")
	placeholder := lipgloss.Color("245")
	input.FocusedStyle.Base = input.FocusedStyle.Base.Background(bg).Foreground(fg)
	input.FocusedStyle.Text = input.FocusedStyle.Text.Background(bg).Foreground(fg)
	input.FocusedStyle.Placeholder = input.FocusedStyle.Placeholder.Background(bg).Foreground(placeholder)
	input.FocusedStyle.CursorLine = input.FocusedStyle.CursorLine.Background(bg)
	input.FocusedStyle.EndOfBuffer = input.FocusedStyle.EndOfBuffer.Background(bg).Foreground(bg)
	input.BlurredStyle.Base = input.BlurredStyle.Base.Background(bg).Foreground(fg)
	input.BlurredStyle.Text = input.BlurredStyle.Text.Background(bg).Foreground(fg)
	input.BlurredStyle.Placeholder = input.BlurredStyle.Placeholder.Background(bg).Foreground(placeholder)
	input.BlurredStyle.CursorLine = input.BlurredStyle.CursorLine.Background(bg)
	input.BlurredStyle.EndOfBuffer = input.BlurredStyle.EndOfBuffer.Background(bg).Foreground(bg)

	messageView := viewport.New(0, 0)

	m := model{
		loop:               loop,
		profileName:        strings.TrimSpace(opts.ProfileName),
		sessionKey:         opts.SessionKey,
		stream:             opts.Stream,
		input:              input,
		messageView:        messageView,
		status:             "Ready",
		activity:           "Idle",
		headerHeight:       3,
		footerHeight:       2,
		approvalHeight:     0,
		activeAssistantIdx: -1,
		activeReasoningIdx: -1,
		activeToolIdx:      -1,
	}
	return m
}

func (m model) Init() tea.Cmd {
	return textarea.Blink
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.input.SetWidth(maxInt(10, msg.Width-4))
		m.refreshLayout()
		return m, nil
	case runtimeEventMsg:
		m.handleRuntimeEvent(msg.event)
		return m, nil
	case processDoneMsg:
		m.busy = false
		if msg.err != nil {
			switch err := msg.err.(type) {
			case *agent.ApprovalDeniedError:
				m.status = "Approval denied"
				m.appendBlock(roleError, "Approval denied", agentclirun.FormatDeniedError(err))
			case *agent.ApprovalRequiredError:
				m.status = "Approval required"
				m.appendBlock(roleError, "Approval required", agentclirun.FormatApprovalError(err))
			default:
				m.status = "Error"
				m.appendBlock(roleError, "Error", msg.err.Error())
			}
			m.streamingSeen = false
			m.activeAssistantIdx = -1
			m.activeReasoningIdx = -1
			return m, nil
		}
		if !m.streamingSeen && strings.TrimSpace(msg.response) != "" {
			m.appendBlock(roleAssistant, "Assistant", strings.TrimSpace(msg.response))
		} else if m.streamingSeen {
			m.normalizeActiveAssistant()
		}
		m.streamingSeen = false
		m.activeAssistantIdx = -1
		m.activeReasoningIdx = -1
		m.activeToolIdx = -1
		m.status = "Ready"
		return m, nil
	case approvalPromptMsg:
		m.awaitingApproval = true
		m.approvalRequest = msg.request
		m.approvalReply = msg.reply
		m.status = "Waiting for approval"
		m.refreshLayout()
		return m, nil
	case tea.KeyMsg:
		if m.awaitingApproval {
			switch strings.ToLower(msg.String()) {
			case "y":
				m.approvalReply <- true
				close(m.approvalReply)
				m.awaitingApproval = false
				m.approvalReply = nil
				m.status = "Approval granted"
				m.refreshLayout()
				return m, nil
			case "n", "esc":
				m.approvalReply <- false
				close(m.approvalReply)
				m.awaitingApproval = false
				m.approvalReply = nil
				m.status = "Approval denied"
				m.refreshLayout()
				return m, nil
			default:
				return m, nil
			}
		}

		switch msg.Type {
		case tea.KeyCtrlC:
			return m, tea.Quit
		case tea.KeyCtrlJ:
			m.input.InsertString("\n")
			m.refreshLayout()
			return m, nil
		case tea.KeyCtrlR:
			m.reasoningExpanded = !m.reasoningExpanded
			m.rebuildTranscript()
			return m, nil
		case tea.KeyEnter:
			if m.busy {
				return m, nil
			}
			line := strings.TrimSpace(m.input.Value())
			if line == "" {
				return m, nil
			}
			m.input.SetValue("")
			m.busy = true
			m.streamingSeen = false
			m.activeAssistantIdx = -1
			m.activeReasoningIdx = -1
			m.status = "Running"
			m.appendBlock(roleUser, "You", line)
			return m, m.processMessageCmd(line)
		}
	case tea.MouseMsg:
		return m.handleMouse(msg)
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.refreshLayout()
	return m, cmd
}

func (m model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.messageView.LineUp(3)
		return m, nil
	case tea.MouseButtonWheelDown:
		m.messageView.LineDown(3)
		return m, nil
	case tea.MouseButtonLeft:
		if msg.Action == tea.MouseActionPress {
			m.input.Focus()
			return m, nil
		}
	}

	var cmd tea.Cmd
	m.messageView, cmd = m.messageView.Update(msg)
	return m, cmd
}

func (m model) View() string {
	if m.width == 0 || m.height == 0 {
		return "\nLoading..."
	}

	bodyStyle := lipgloss.NewStyle()
	body := bodyStyle.
		Width(maxInt(20, m.width-bodyStyle.GetHorizontalFrameSize())).
		Height(maxInt(1, m.messageView.Height)).
		Render(m.messageView.View())

	sections := []string{
		m.headerView(),
		body,
	}
	if m.awaitingApproval {
		sections = append(sections, m.approvalView())
	}
	sections = append(sections, m.inputView(), m.statusView())
	return strings.Join(sections, "\n")
}

func (m model) processMessageCmd(message string) tea.Cmd {
	return func() tea.Msg {
		resp, err := m.loop.Process(context.Background(), m.sessionKey, message)
		return processDoneMsg{response: resp, err: err}
	}
}

func (m *model) handleRuntimeEvent(evt agent.Event) {
	switch evt.Kind {
	case agent.EventModelDelta:
		if !m.stream {
			return
		}
		payload, ok := evt.Payload.(agent.ModelDeltaPayload)
		if !ok || payload.Delta == "" {
			return
		}
		m.appendAssistantDelta(payload.Delta)
		m.streamingSeen = true
	case agent.EventModelReasoning:
		payload, ok := evt.Payload.(agent.ModelReasoningPayload)
		if !ok || payload.Delta == "" {
			return
		}
		m.appendReasoningDelta(payload.Delta)
	default:
		m.updateActivity(evt)
		m.updateToolBlock(evt)
	}
}

func (m *model) appendBlock(role messageRole, title, content string) {
	m.messages = append(m.messages, messageBlock{
		role:    role,
		title:   title,
		content: content,
	})
	m.rebuildTranscript()
}

func (m *model) appendOrUpdateToolBlock(content string) {
	if m.activeToolIdx >= 0 && m.activeToolIdx < len(m.messages) && m.messages[m.activeToolIdx].role == roleSystem && m.messages[m.activeToolIdx].title == "Tool" {
		m.messages[m.activeToolIdx].content = content
		m.rebuildTranscript()
		return
	}
	m.messages = append(m.messages, messageBlock{
		role:    roleSystem,
		title:   "Tool",
		content: content,
	})
	m.activeToolIdx = len(m.messages) - 1
	m.rebuildTranscript()
}

func (m *model) appendAssistantDelta(delta string) {
	if m.activeAssistantIdx < 0 || m.activeAssistantIdx >= len(m.messages) || m.messages[m.activeAssistantIdx].role != roleAssistant {
		m.messages = append(m.messages, messageBlock{
			role:  roleAssistant,
			title: "Assistant",
		})
		m.activeAssistantIdx = len(m.messages) - 1
	}
	m.messages[m.activeAssistantIdx].content += delta
	m.rebuildTranscript()
}

func (m *model) appendReasoningDelta(delta string) {
	if m.activeReasoningIdx < 0 || m.activeReasoningIdx >= len(m.messages) || m.messages[m.activeReasoningIdx].role != roleReasoning {
		m.messages = append(m.messages, messageBlock{
			role:  roleReasoning,
			title: "Reasoning",
		})
		m.activeReasoningIdx = len(m.messages) - 1
	}
	m.messages[m.activeReasoningIdx].content += delta
	m.rebuildTranscript()
}

func (m *model) normalizeActiveAssistant() {
	if m.activeAssistantIdx < 0 || m.activeAssistantIdx >= len(m.messages) {
		return
	}
	m.messages[m.activeAssistantIdx].content = strings.TrimRight(m.messages[m.activeAssistantIdx].content, "\n")
	m.rebuildTranscript()
}

func (m *model) rebuildTranscript() {
	m.messageView.SetContent(m.renderTranscript())
	m.messageView.GotoBottom()
}

func (m model) renderTranscript() string {
	width := maxInt(20, m.messageView.Width)
	blocks := make([]string, 0, len(m.messages))
	for _, block := range m.messages {
		blocks = append(blocks, renderMessageBlock(block, width, m.reasoningExpanded))
	}
	return strings.Join(blocks, "\n\n")
}

func (m model) approvalView() string {
	lines := []string{"Approval required"}
	if m.approvalRequest.Tool != "" {
		lines = append(lines, "Tool: "+m.approvalRequest.Tool)
	}
	if m.approvalRequest.ActionLabel != "" {
		lines = append(lines, "Action: "+m.approvalRequest.ActionLabel)
	}
	if m.approvalRequest.Reason != "" {
		lines = append(lines, "Reason: "+m.approvalRequest.Reason)
	}
	if command, ok := m.approvalRequest.Details["command"].(string); ok && command != "" {
		lines = append(lines, "Command: "+command)
	}
	if path, ok := m.approvalRequest.Details["resolved_path"].(string); ok && path != "" {
		lines = append(lines, "Path: "+path)
	}
	lines = append(lines, "[y] approve  [n/esc] deny")
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("214")).
		Padding(0, 1).
		Render(strings.Join(lines, "\n"))
}

func (m model) statusView() string {
	parts := []string{m.status}
	if m.busy {
		parts = append(parts, "busy")
	}
	if m.stream {
		parts = append(parts, "stream")
	}
	if m.activity != "" {
		parts = append(parts, m.activity)
	}
	if m.hasReasoning() {
		if m.reasoningExpanded {
			parts = append(parts, "reasoning:expanded")
		} else {
			parts = append(parts, "reasoning:collapsed")
		}
	}
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color("250")).
		Render(strings.Join(parts, " | "))
}

func (m model) headerView() string {
	leftParts := []string{"agentcli"}
	if m.profileName != "" {
		leftParts = append(leftParts, "profile="+m.profileName)
	}
	left := strings.Join(leftParts, " | ")
	rightParts := []string{
		"session=" + m.sessionKey,
	}
	if m.busy {
		rightParts = append(rightParts, "running")
	}
	if m.awaitingApproval {
		rightParts = append(rightParts, "approval")
	}
	right := strings.Join(rightParts, " | ")
	return lipgloss.JoinHorizontal(
		lipgloss.Top,
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("69")).Width(maxInt(10, m.width-lipgloss.Width(right)-1)).Render(left),
		lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render(right),
	)
}

func (m model) inputView() string {
	style := lipgloss.NewStyle().
		Background(lipgloss.Color("236")).
		Foreground(lipgloss.Color("252")).
		Padding(1, 2)
	return style.
		Width(maxInt(20, m.width)).
		Render(m.input.View())
}

func (m *model) refreshLayout() {
	if m.width == 0 || m.height == 0 {
		return
	}

	inputStyle := lipgloss.NewStyle().Padding(1, 2)
	m.footerHeight = inputStyle.GetVerticalFrameSize() + m.input.Height() + 1

	m.approvalHeight = 0
	if m.awaitingApproval {
		m.approvalHeight = 6
	}

	bodyHeight := m.height - m.headerHeight - m.footerHeight - m.approvalHeight
	if bodyHeight < 8 {
		bodyHeight = 8
	}

	bodyStyle := lipgloss.NewStyle()
	m.messageView.Width = maxInt(20, m.width-bodyStyle.GetHorizontalFrameSize())
	m.messageView.Height = maxInt(1, bodyHeight-bodyStyle.GetVerticalFrameSize())
	m.messageView.SetContent(m.renderTranscript())
	m.messageView.GotoBottom()
}

func renderMessageBlock(block messageBlock, width int, reasoningExpanded bool) string {
	titleStyle := lipgloss.NewStyle().Bold(true)
	boxStyle := lipgloss.NewStyle().
		Padding(0, 1)

	switch block.role {
	case roleUser:
		titleStyle = titleStyle.Foreground(lipgloss.Color("81"))
		boxStyle = boxStyle.
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("81"))
	case roleAssistant:
		titleStyle = titleStyle.Foreground(lipgloss.Color("42"))
		boxStyle = boxStyle.
			Align(lipgloss.Left).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("42"))
	case roleReasoning:
		titleStyle = titleStyle.Foreground(lipgloss.Color("214"))
		boxStyle = boxStyle.
			Align(lipgloss.Left).
			Border(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("214"))
	case roleError:
		titleStyle = titleStyle.Foreground(lipgloss.Color("203"))
		boxStyle = boxStyle.
			Align(lipgloss.Left).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("203"))
	default:
		titleStyle = titleStyle.Foreground(lipgloss.Color("245"))
		boxStyle = boxStyle.
			Align(lipgloss.Left).
			Border(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("240"))
	}

	contentWidth := maxInt(16, width-boxStyle.GetHorizontalFrameSize())
	bodyStyle := lipgloss.NewStyle().Width(contentWidth)

	body := strings.TrimSpace(block.content)
	if body == "" {
		body = " "
	}
	if block.role == roleReasoning && !reasoningExpanded {
		body = summarizeReasoning(body)
	}
	return boxStyle.Width(contentWidth).Render(
		titleStyle.Render(block.title) + "\n" +
			bodyStyle.Render(body),
	)
}

func summarizeReasoning(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return "Thinking...  (Ctrl+R to expand)"
	}
	singleLine := strings.Join(strings.Fields(body), " ")
	return truncateForStatus(singleLine, 120) + "  (Ctrl+R to expand)"
}

func (m model) hasReasoning() bool {
	for _, block := range m.messages {
		if block.role == roleReasoning {
			return true
		}
	}
	return false
}

func (m *model) updateActivity(evt agent.Event) {
	switch evt.Kind {
	case agent.EventTurnStarted:
		payload, ok := evt.Payload.(agent.TurnStartedPayload)
		if ok {
			m.activity = "User: " + truncateForStatus(payload.UserMessage, 48)
		}
	case agent.EventModelRequest:
		payload, ok := evt.Payload.(agent.ModelRequestPayload)
		if ok {
			m.activity = "Thinking with " + payload.Model
		}
	case agent.EventToolStarted:
		payload, ok := evt.Payload.(agent.ToolStartedPayload)
		if ok {
			m.activity = "Running " + payload.Tool
		}
	case agent.EventToolFinished:
		payload, ok := evt.Payload.(agent.ToolFinishedPayload)
		if ok {
			if payload.IsError {
				m.activity = payload.Tool + " failed"
			} else {
				m.activity = payload.Tool + " finished"
			}
		}
	case agent.EventApprovalRequested:
		payload, ok := evt.Payload.(agent.ApprovalRequestedPayload)
		if ok {
			m.activity = "Approval needed for " + payload.Tool
		}
	case agent.EventApprovalResolved:
		payload, ok := evt.Payload.(agent.ApprovalResolvedPayload)
		if ok {
			if payload.Approved {
				m.activity = "Approved " + payload.Tool
			} else {
				m.activity = "Denied " + payload.Tool
			}
		}
	case agent.EventError:
		payload, ok := evt.Payload.(agent.ErrorPayload)
		if ok {
			m.activity = "Error: " + truncateForStatus(payload.Message, 48)
		}
	case agent.EventTurnFinished:
		m.activity = "Idle"
	}
}

func (m *model) updateToolBlock(evt agent.Event) {
	switch evt.Kind {
	case agent.EventToolStarted:
		payload, ok := evt.Payload.(agent.ToolStartedPayload)
		if !ok {
			return
		}
		m.appendOrUpdateToolBlock(formatToolStartedBlock(payload))
	case agent.EventToolFinished:
		payload, ok := evt.Payload.(agent.ToolFinishedPayload)
		if !ok {
			return
		}
		m.appendOrUpdateToolBlock(formatToolFinishedBlock(payload))
		m.activeToolIdx = -1
	case agent.EventApprovalRequested:
		payload, ok := evt.Payload.(agent.ApprovalRequestedPayload)
		if !ok {
			return
		}
		m.appendOrUpdateToolBlock("Waiting for approval: " + payload.Tool)
	case agent.EventApprovalResolved:
		payload, ok := evt.Payload.(agent.ApprovalResolvedPayload)
		if !ok {
			return
		}
		if payload.Approved {
			m.appendOrUpdateToolBlock("Approved: " + payload.Tool)
		} else {
			m.appendOrUpdateToolBlock("Denied: " + payload.Tool)
			m.activeToolIdx = -1
		}
	}
}

func formatToolStartedBlock(payload agent.ToolStartedPayload) string {
	switch payload.Tool {
	case "read_file":
		return "Reading file\n" + formatPathArg(payload.Arguments)
	case "edit_file":
		return "Editing file\n" + formatPathArg(payload.Arguments)
	case "write_file":
		return "Writing file\n" + formatPathArg(payload.Arguments)
	case "web_fetch":
		if rawURL, ok := payload.Arguments["url"].(string); ok && rawURL != "" {
			return "Fetching URL\n" + truncateForStatus(rawURL, 120)
		}
		return "Fetching URL"
	case "bash":
		if command, ok := payload.Arguments["command"].(string); ok && command != "" {
			return "Running shell command\n" + truncateForStatus(command, 140)
		}
		return "Running shell command"
	default:
		if len(payload.Arguments) == 0 {
			return "Running " + payload.Tool
		}
		return "Running " + payload.Tool + "\n" + summarizeToolArguments(payload.Arguments)
	}
}

func formatToolFinishedBlock(payload agent.ToolFinishedPayload) string {
	switch payload.Tool {
	case "read_file":
		return formatReadFileFinished(payload)
	case "edit_file":
		return formatEditFileFinished(payload)
	case "write_file":
		return formatWriteFileFinished(payload)
	case "web_fetch":
		return formatWebFetchFinished(payload)
	case "bash":
		return formatBashFinished(payload)
	}

	if payload.IsError {
		if payload.ErrorText != "" {
			return "Failed " + payload.Tool + "\n" + truncateForStatus(payload.ErrorText, 120)
		}
		return "Failed " + payload.Tool
	}
	if payload.UserPreview != "" {
		return "Finished " + payload.Tool + "\n" + truncateForStatus(payload.UserPreview, 120)
	}
	return "Finished " + payload.Tool
}

func summarizeToolArguments(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	summaries := make([]string, 0, 2)
	if command, ok := args["command"].(string); ok && command != "" {
		summaries = append(summaries, "command: "+truncateForStatus(command, 100))
	}
	if path, ok := args["path"].(string); ok && path != "" {
		summaries = append(summaries, "path: "+truncateForStatus(path, 100))
	}
	if len(summaries) == 0 {
		line := agentclirun.FormatEventLine(agent.Event{
			Kind: agent.EventToolStarted,
			Payload: agent.ToolStartedPayload{
				Tool:      "",
				Arguments: args,
			},
		})
		return truncateForStatus(line, 100)
	}
	return strings.Join(summaries, "\n")
}

func formatPathArg(args map[string]any) string {
	if path, ok := args["path"].(string); ok && path != "" {
		return truncateForStatus(path, 120)
	}
	return "path unavailable"
}

func formatReadFileFinished(payload agent.ToolFinishedPayload) string {
	path, _ := payload.Metadata["path"].(string)
	bytes, _ := intMetadata(payload.Metadata, "bytes")
	truncated, _ := boolMetadata(payload.Metadata, "truncated")
	if payload.IsError {
		if path != "" {
			return "Failed to read " + path + "\n" + truncateForStatus(payload.ErrorText, 120)
		}
		return "Failed read_file\n" + truncateForStatus(payload.ErrorText, 120)
	}
	line := "Read file"
	if path != "" {
		line = "Read " + path
	}
	if bytes > 0 {
		line += " • " + humanBytes(bytes)
	}
	if truncated {
		line += " • truncated"
	}
	return line
}

func formatEditFileFinished(payload agent.ToolFinishedPayload) string {
	path, _ := payload.Metadata["path"].(string)
	replacements, _ := intMetadata(payload.Metadata, "replacements")
	if payload.IsError {
		if path != "" {
			return "Failed to edit " + path + "\n" + truncateForStatus(payload.ErrorText, 120)
		}
		return "Failed edit_file\n" + truncateForStatus(payload.ErrorText, 120)
	}
	line := "Edited file"
	if path != "" {
		line = "Edited " + path
	}
	if replacements > 0 {
		line += " • replacements=" + fmtInt(replacements)
	}
	return line
}

func formatWriteFileFinished(payload agent.ToolFinishedPayload) string {
	path, _ := payload.Metadata["path"].(string)
	bytes, _ := intMetadata(payload.Metadata, "bytes")
	if payload.IsError {
		if path != "" {
			return "Failed to write " + path + "\n" + truncateForStatus(payload.ErrorText, 120)
		}
		return "Failed write_file\n" + truncateForStatus(payload.ErrorText, 120)
	}
	line := "Wrote file"
	if path != "" {
		line = "Wrote " + path
	}
	if bytes > 0 {
		line += " • " + humanBytes(bytes)
	}
	return line
}

func formatWebFetchFinished(payload agent.ToolFinishedPayload) string {
	rawURL, _ := payload.Metadata["url"].(string)
	statusCode, _ := intMetadata(payload.Metadata, "status_code")
	bodyBytes, _ := intMetadata(payload.Metadata, "body_bytes")
	contentType, _ := payload.Metadata["content_type"].(string)
	truncated, _ := boolMetadata(payload.Metadata, "truncated")
	if payload.IsError {
		if rawURL != "" {
			return "Failed to fetch\n" + truncateForStatus(rawURL, 100) + "\n" + truncateForStatus(payload.ErrorText, 120)
		}
		return "Failed web_fetch\n" + truncateForStatus(payload.ErrorText, 120)
	}
	line := "Fetched URL"
	if rawURL != "" {
		line = "Fetched " + truncateForStatus(rawURL, 100)
	}
	details := make([]string, 0, 3)
	if statusCode > 0 {
		details = append(details, fmtInt(statusCode))
	}
	if contentType != "" {
		details = append(details, truncateForStatus(contentType, 32))
	}
	if bodyBytes > 0 {
		details = append(details, humanBytes(bodyBytes))
	}
	if truncated {
		details = append(details, "truncated")
	}
	if len(details) > 0 {
		line += "\n" + strings.Join(details, " • ")
	}
	return line
}

func formatBashFinished(payload agent.ToolFinishedPayload) string {
	command, _ := payload.Metadata["command"].(string)
	exitCode, hasExitCode := intMetadata(payload.Metadata, "exit_code")
	outputBytes, _ := intMetadata(payload.Metadata, "output_bytes")
	truncated, _ := boolMetadata(payload.Metadata, "truncated")
	timedOut, _ := boolMetadata(payload.Metadata, "timed_out")
	outputSample, _ := payload.Metadata["output_sample"].(string)

	header := "Ran shell command"
	if payload.IsError {
		header = "Shell command failed"
	}

	lines := []string{header}
	if command != "" {
		lines = append(lines, truncateForStatus(command, 140))
	}

	details := make([]string, 0, 4)
	if hasExitCode {
		details = append(details, "exit="+fmtInt(exitCode))
	}
	if timedOut {
		details = append(details, "timed out")
	}
	if outputBytes > 0 {
		details = append(details, humanBytes(outputBytes))
	}
	if truncated {
		details = append(details, "truncated")
	}
	if len(details) > 0 {
		lines = append(lines, strings.Join(details, " • "))
	}
	if payload.IsError && payload.ErrorText != "" {
		lines = append(lines, truncateForStatus(payload.ErrorText, 120))
	} else if outputSample != "" {
		lines = append(lines, truncateForStatus(outputSample, 140))
	}
	return strings.Join(lines, "\n")
}

func humanBytes(n int) string {
	if n < 1024 {
		return fmtInt(n) + " B"
	}
	if n < 1024*1024 {
		return fmtInt(n/1024) + " KB"
	}
	return fmtInt(n/(1024*1024)) + " MB"
}

func fmtInt(n int) string {
	return strconv.Itoa(n)
}

func truncateForStatus(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}

func intMetadata(metadata map[string]any, key string) (int, bool) {
	if metadata == nil {
		return 0, false
	}
	switch value := metadata[key].(type) {
	case int:
		return value, true
	case int64:
		return int(value), true
	case float64:
		return int(value), true
	default:
		return 0, false
	}
}

func boolMetadata(metadata map[string]any, key string) (bool, bool) {
	if metadata == nil {
		return false, false
	}
	value, ok := metadata[key].(bool)
	return value, ok
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
