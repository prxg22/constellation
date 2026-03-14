package mux

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
)

const titleGenerating = "(generating)"

var statusMarkerRe = regexp.MustCompile(`\[STATUS:\s*([^\]]+)\]`)

// parseStatusMarker extracts [STATUS: ...] markers from text.
func parseStatusMarker(text string) (cleaned string, status string) {
	match := statusMarkerRe.FindStringSubmatch(text)
	if match == nil {
		return text, ""
	}
	status = strings.TrimSpace(match[1])
	cleaned = statusMarkerRe.ReplaceAllString(text, "")
	return cleaned, status
}

// resolveAgentBinary returns the path to the CLI binary for the given agent.
func resolveAgentBinary(agent string) (string, error) {
	bin := "claude"
	switch agent {
	case "codex":
		bin = "codex"
	case "opencode":
		bin = "opencode"
	case "cursor":
		bin = "agent"
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return "", fmt.Errorf("%s not found on PATH: %w", bin, err)
	}
	return path, nil
}

// Send sends a prompt to an agent, spawning a subprocess.
func (m *Manager) Send(req SendRequest) (*SendResult, error) {
	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = uuid.New().String()
	}
	agent := req.Agent
	if agent == "" {
		agent = "claude"
	}

	now := nowUnix()
	tx, err := m.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	insRes, _ := tx.Exec(`INSERT OR IGNORE INTO sessions (id, status, handoff_path, conversation_id, token_usage, created_at, last_active_at)
		VALUES (?, ?, '', NULL, NULL, ?, ?)`, sessionID, StatusActive, now, now)
	if n, _ := insRes.RowsAffected(); n > 0 {
		m.broadcast.PublishSessionCreated(sessionID, "")
	}

	// Generate title for new sessions
	res, _ := tx.Exec(`UPDATE sessions SET title = ? WHERE id = ? AND (title IS NULL OR title = '')`, titleGenerating, sessionID)
	if affected, _ := res.RowsAffected(); affected == 1 {
		go func(sid, p string) {
			title := m.generateTitle(p)
			_, _ = m.db.Exec(`UPDATE sessions SET title = ? WHERE id = ?`, title, sid)
		}(sessionID, req.Prompt)
	}

	// Persist user message
	var userMsgID int64
	msgRes, _ := tx.Exec(`INSERT INTO messages (session_id, role, content, created_at) VALUES (?, 'user', ?, ?)`,
		sessionID, req.Prompt, now)
	if msgRes != nil {
		userMsgID, _ = msgRes.LastInsertId()
	}

	// Resolve attachments
	var attachments []AttachmentRef
	if len(req.AttachmentIDs) > 0 {
		attachments, err = m.ResolveAttachments(sessionID, req.AttachmentIDs)
		if err != nil {
			return nil, fmt.Errorf("resolve attachments: %w", err)
		}
	}

	m.appendConversation(sessionID, ConversationEntry{
		Role:        "user",
		Content:     req.Prompt,
		Attachments: attachments,
	})

	var conversationID, handoffPath, lastAgent string
	err = tx.QueryRow(`SELECT COALESCE(conversation_id, ''), COALESCE(handoff_path, ''), COALESCE(last_agent, '') FROM sessions WHERE id = ?`, sessionID).Scan(&conversationID, &handoffPath, &lastAgent)
	if err != nil {
		return nil, fmt.Errorf("query session: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	prompt := req.Prompt

	// Resume or inject context
	canResume := conversationID != "" && lastAgent == agent
	if !canResume {
		if ctx := m.buildContext(sessionID, handoffPath); ctx != "" {
			prompt = fmt.Sprintf("Previous session context:\n%s\n\nNew request: %s", ctx, prompt)
		}
		conversationID = ""
	}

	m.setUserMessage(sessionID, req.Prompt)

	agentPath, err := resolveAgentBinary(agent)
	if err != nil {
		return nil, err
	}

	// Build command based on agent type
	var parser func(io.Reader, chan<- ChanEvent) streamResult
	var cmd *exec.Cmd

	switch agent {
	case "codex":
		cmd, parser = m.buildCodexCommand(agentPath, sessionID, prompt, req, attachments)
	case "opencode":
		cmd, parser = m.buildOpenCodeCommand(agentPath, sessionID, prompt, req, conversationID, attachments)
	case "cursor":
		cmd, parser = m.buildCursorCommand(agentPath, sessionID, prompt, req, conversationID, attachments)
	default: // claude
		cmd, parser = m.buildClaudeCommand(agentPath, sessionID, prompt, req, conversationID, attachments)
	}

	if m.config.AgentEnv != nil {
		cmd.Env = m.config.AgentEnv()
	} else {
		cmd.Env = os.Environ()
	}
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", agent, err)
	}

	m.mu.Lock()
	m.activeProcesses[sessionID] = &processEntry{
		Pid:     cmd.Process.Pid,
		Kill:    func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) },
		AgentID: agent,
	}
	delete(m.stoppedSessions, sessionID)
	m.mu.Unlock()
	_, _ = m.db.Exec(`UPDATE sessions SET pid = ? WHERE id = ?`, cmd.Process.Pid, sessionID)

	m.broadcast.PublishStatus(sessionID, "active", "Thinking...", "", req.Prompt, m.QueueLength(sessionID), m.IsQueuePaused(sessionID))

	ch := make(chan ChanEvent, 32)
	go m.runAgentLoop(sessionID, agent, req, cmd, stdoutPipe, parser, ch)

	return &SendResult{
		Events:    ch,
		SessionID: sessionID,
		MessageID: userMsgID,
	}, nil
}

// runAgentLoop reads output, persists results, and handles post-completion.
func (m *Manager) runAgentLoop(sessionID, agent string, req SendRequest, cmd *exec.Cmd, stdout io.ReadCloser, parser func(io.Reader, chan<- ChanEvent) streamResult, ch chan ChanEvent) {
	defer close(ch)
	result := parser(stdout, ch)
	cmd.Wait()

	m.clearAction(sessionID)
	m.mu.Lock()
	delete(m.activeProcesses, sessionID)
	wasStopped := m.stoppedSessions[sessionID]
	delete(m.stoppedSessions, sessionID)
	m.mu.Unlock()
	_, _ = m.db.Exec(`UPDATE sessions SET pid = 0 WHERE id = ?`, sessionID)

	now := nowUnix()
	if result.ConversationID != "" {
		_, _ = m.db.Exec(`UPDATE sessions SET conversation_id = ?, token_usage = ?, last_active_at = ?, last_agent = ?, last_agent_sub = ?, last_model = ?, last_effort = ? WHERE id = ?`,
			result.ConversationID, result.TokenUsage, now, agent, req.AgentSub, req.Model, req.Effort, sessionID)
	} else {
		_, _ = m.db.Exec(`UPDATE sessions SET token_usage = ?, last_active_at = ?, last_agent = ?, last_agent_sub = ?, last_model = ?, last_effort = ? WHERE id = ?`,
			result.TokenUsage, now, agent, req.AgentSub, req.Model, req.Effort, sessionID)
	}

	if result.FullText != "" {
		_, _ = m.db.Exec(`INSERT INTO messages (session_id, role, content, created_at) VALUES (?, 'assistant', ?, ?)`,
			sessionID, result.FullText, now)
		m.appendConversation(sessionID, ConversationEntry{
			Role: "assistant", Agent: agent, Content: result.FullText,
		})
	}

	if wasStopped {
		return
	}

	// Handle ask_user
	m.mu.Lock()
	pending, isAskUser := m.askUserPending[sessionID]
	if isAskUser {
		delete(m.askUserPending, sessionID)
	}
	m.mu.Unlock()
	if isAskUser {
		m.handleAskUserCompletion(sessionID, pending)
		return
	}

	if result.UsagePct > 0.50 {
		m.triggerHandoff(sessionID)
	} else {
		m.resetIdleTimer(sessionID)
	}

	go m.ProcessNextFromQueue(sessionID)
}

// generateTitle creates a short title from the user prompt.
func (m *Manager) generateTitle(prompt string) string {
	if m.config.TitleGen != nil {
		return m.config.TitleGen.GenerateTitle(prompt)
	}
	if len(prompt) > 500 {
		prompt = prompt[:500]
	}
	agentPath, err := resolveAgentBinary("claude")
	if err != nil {
		return fallbackTitle(prompt)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, agentPath, "-p", "--", fmt.Sprintf(
		"Summarize this user request in 5 words or fewer. Output ONLY the summary, nothing else:\n%s", prompt))
	if m.config.AgentEnv != nil {
		cmd.Env = m.config.AgentEnv()
	}
	out, err := cmd.Output()
	if err != nil {
		return fallbackTitle(prompt)
	}
	title := strings.TrimSpace(string(out))
	title = strings.Trim(title, `"'`)
	if title == "" {
		return fallbackTitle(prompt)
	}
	if len(title) > 50 {
		title = title[:50]
	}
	return title
}

// buildClaudeCommand builds the exec.Cmd for claude agent.
func (m *Manager) buildClaudeCommand(agentPath, sessionID, prompt string, req SendRequest, conversationID string, attachments []AttachmentRef) (*exec.Cmd, func(io.Reader, chan<- ChanEvent) streamResult) {
	mcpConfig := ""
	if m.config.MCPProvider != nil {
		mcpConfig, _ = m.config.MCPProvider.MCPConfig(sessionID, "claude")
	}

	args := []string{"-p", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions"}

	if mcpConfig != "" {
		mcpDir := filepath.Join(os.TempDir(), "constellation-mcp")
		os.MkdirAll(mcpDir, 0755)
		mcpPath := filepath.Join(mcpDir, fmt.Sprintf("mcp-%s.json", sessionID))
		os.WriteFile(mcpPath, []byte(mcpConfig), 0644)
		args = append(args, "--mcp-config", mcpPath)
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if req.Effort != "" {
		args = append(args, "--effort", req.Effort)
	}
	if req.AgentSub != "" && req.AgentSub != "default" {
		args = append(args, "--agent", req.AgentSub)
	}
	if conversationID != "" {
		args = append(args, "--resume", conversationID)
	}
	prompt = buildAttachmentPrompt(prompt, attachments)
	args = append(args, "--", prompt)

	cmd := exec.Command(agentPath, args...)
	parser := func(r io.Reader, ch chan<- ChanEvent) streamResult {
		return m.streamClaudeOutput(sessionID, r, ch)
	}
	return cmd, parser
}

func (m *Manager) buildCodexCommand(agentPath, sessionID, prompt string, req SendRequest, attachments []AttachmentRef) (*exec.Cmd, func(io.Reader, chan<- ChanEvent) streamResult) {
	args := []string{"exec", "--json", "--dangerously-bypass-approvals-and-sandbox"}
	if req.Model != "" {
		args = append(args, "-m", req.Model)
	}
	prompt = buildAttachmentPrompt(prompt, attachments)
	args = append(args, "--", prompt)
	cmd := exec.Command(agentPath, args...)
	parser := func(r io.Reader, ch chan<- ChanEvent) streamResult {
		return m.streamCodexOutput(sessionID, r, ch)
	}
	return cmd, parser
}

func (m *Manager) buildOpenCodeCommand(agentPath, sessionID, prompt string, req SendRequest, conversationID string, attachments []AttachmentRef) (*exec.Cmd, func(io.Reader, chan<- ChanEvent) streamResult) {
	args := []string{"run", "--format", "json"}
	if req.Model != "" {
		args = append(args, "-m", req.Model)
	}
	if req.Effort != "" {
		args = append(args, "--variant", req.Effort)
	}
	if req.AgentSub != "" && req.AgentSub != "default" {
		args = append(args, "--agent", req.AgentSub)
	}
	if conversationID != "" {
		args = append(args, "-s", conversationID)
	}
	for _, att := range attachments {
		args = append(args, "--file", att.Path)
	}
	args = append(args, "--", prompt)
	cmd := exec.Command(agentPath, args...)
	parser := func(r io.Reader, ch chan<- ChanEvent) streamResult {
		return m.streamOpenCodeOutput(sessionID, r, ch)
	}
	return cmd, parser
}

func (m *Manager) buildCursorCommand(agentPath, sessionID, prompt string, req SendRequest, conversationID string, attachments []AttachmentRef) (*exec.Cmd, func(io.Reader, chan<- ChanEvent) streamResult) {
	mcpConfig := ""
	if m.config.MCPProvider != nil {
		mcpConfig, _ = m.config.MCPProvider.MCPConfig(sessionID, "cursor")
	}

	cursorWorkspace := filepath.Join(os.TempDir(), "constellation-cursor")
	cursorDir := filepath.Join(cursorWorkspace, ".cursor")
	os.MkdirAll(cursorDir, 0755)
	if mcpConfig != "" {
		os.WriteFile(filepath.Join(cursorDir, "mcp.json"), []byte(mcpConfig), 0644)
	}

	args := []string{"-p", "--output-format", "stream-json", "--stream-partial-output", "--force", "--approve-mcps", "--trust", "--workspace", cursorWorkspace}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if conversationID != "" {
		args = append(args, "--resume", conversationID)
	}
	prompt = buildAttachmentPrompt(prompt, attachments)
	args = append(args, "--", prompt)

	cmd := exec.Command(agentPath, args...)
	cmd.Dir = cursorWorkspace
	parser := func(r io.Reader, ch chan<- ChanEvent) streamResult {
		return m.streamCursorOutput(sessionID, r, ch)
	}
	return cmd, parser
}

// buildAttachmentPrompt prepends file path references to a prompt.
func buildAttachmentPrompt(prompt string, attachments []AttachmentRef) string {
	if len(attachments) == 0 {
		return prompt
	}
	var sb strings.Builder
	sb.WriteString("[Attached files — use the Read tool to view them:\n")
	for _, att := range attachments {
		sb.WriteString(fmt.Sprintf("- %s\n", att.Path))
	}
	sb.WriteString("]\n\n")
	sb.WriteString(prompt)
	return sb.String()
}

// Helper methods for user message tracking
func (m *Manager) setUserMessage(sessionID, msg string) {
	m.mu.Lock()
	m.lastUserMessage[sessionID] = msg
	m.mu.Unlock()
}

func (m *Manager) getUserMessage(sessionID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastUserMessage[sessionID]
}

func (m *Manager) clearUserMessage(sessionID string) {
	m.mu.Lock()
	delete(m.lastUserMessage, sessionID)
	m.mu.Unlock()
}

// trackAction records a tool action for status tracking.
func (m *Manager) trackAction(sessionID, tool string, args map[string]interface{}) {
	summary := m.config.ActionFmt.FormatAction(tool, args)
	userMsg := m.getUserMessage(sessionID)
	m.mu.Lock()
	m.lastActions[sessionID] = &ActionStatus{
		Status: "active", Summary: summary, Tool: tool, UpdatedAt: nowUnix(),
	}
	m.mu.Unlock()
	m.broadcast.PublishStatus(sessionID, "active", summary, tool, userMsg, m.QueueLength(sessionID), m.IsQueuePaused(sessionID))
}

func (m *Manager) clearAction(sessionID string) {
	m.mu.Lock()
	var lastSummary string
	if a, ok := m.lastActions[sessionID]; ok {
		lastSummary = a.Summary
		a.Tool = ""
		a.Status = "idle"
		a.UpdatedAt = nowUnix()
	}
	m.mu.Unlock()
	m.clearUserMessage(sessionID)
	m.broadcast.PublishStatus(sessionID, "idle", lastSummary, "", "", m.QueueLength(sessionID), m.IsQueuePaused(sessionID))
}

// processTextWithStatus extracts [STATUS:] markers and updates action tracking.
func (m *Manager) processTextWithStatus(sessionID, text string) string {
	cleaned, status := parseStatusMarker(text)
	if status != "" {
		m.mu.Lock()
		if existing, ok := m.lastActions[sessionID]; ok {
			existing.Summary = status
			existing.UpdatedAt = nowUnix()
		} else {
			m.lastActions[sessionID] = &ActionStatus{
				Status:    "active",
				Summary:   status,
				UpdatedAt: nowUnix(),
			}
		}
		m.mu.Unlock()
		m.broadcast.PublishStatus(sessionID, "active", status, "", m.getUserMessage(sessionID), m.QueueLength(sessionID), m.IsQueuePaused(sessionID))
	}
	return cleaned
}

// buildContext returns context text for a session when --resume can't be used.
func (m *Manager) buildContext(sessionID, handoffPath string) string {
	if handoffPath != "" {
		if summary, err := os.ReadFile(handoffPath); err == nil && len(summary) > 0 {
			return string(summary)
		}
	}
	rows, err := m.db.Query(
		`SELECT role, content FROM messages WHERE session_id = ? ORDER BY id DESC LIMIT 20 OFFSET 1`, sessionID)
	if err != nil {
		return ""
	}
	defer rows.Close()
	var msgs []Message
	for rows.Next() {
		var msg Message
		if rows.Scan(&msg.Role, &msg.Content) == nil {
			msgs = append(msgs, msg)
		}
	}
	if len(msgs) == 0 {
		return ""
	}
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	var sb strings.Builder
	for _, msg := range msgs {
		role := "User"
		if msg.Role == "assistant" {
			role = "Assistant"
		}
		sb.WriteString(fmt.Sprintf("%s: %s\n\n", role, msg.Content))
	}
	return sb.String()
}

// handleAskUserCompletion broadcasts ask_user state when agent completes with a pending question.
func (m *Manager) handleAskUserCompletion(sessionID string, pending AskUserPending) {
	m.appendConversation(sessionID, ConversationEntry{
		Role: "ask_user", Origin: "agent", DisplayAs: "ask_user",
		Content: pending.Question,
		Input:   map[string]interface{}{"options": pending.Options},
	})
	m.broadcast.PublishSessionEvent(sessionID, SSEMessage, map[string]interface{}{
		"type": "ask_user", "question": pending.Question,
	})
	m.broadcast.PublishStatus(sessionID, "waiting", "Waiting for user input", "", m.getUserMessage(sessionID), m.QueueLength(sessionID), m.IsQueuePaused(sessionID))
	_, _ = m.db.Exec(`UPDATE sessions SET status = ? WHERE id = ?`, "waiting", sessionID)
}

// ResolveAttachments resolves attachment IDs to AttachmentRef.
func (m *Manager) ResolveAttachments(sessionID string, ids []string) ([]AttachmentRef, error) {
	attDir := filepath.Join(m.sessionDir(sessionID), "attachments")
	entries, err := os.ReadDir(attDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no attachments directory for session %s", sessionID)
		}
		return nil, err
	}
	lookup := make(map[string]os.DirEntry)
	for _, e := range entries {
		parts := strings.SplitN(e.Name(), "_", 3)
		if len(parts) >= 2 {
			id := parts[0] + "_" + parts[1]
			lookup[id] = e
		}
	}
	var refs []AttachmentRef
	for _, id := range ids {
		entry, ok := lookup[id]
		if !ok {
			return nil, fmt.Errorf("attachment %s not found in session %s", id, sessionID)
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		absPath, _ := filepath.Abs(filepath.Join(attDir, entry.Name()))
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		attType := "document"
		switch ext {
		case ".jpg", ".jpeg", ".png", ".gif", ".webp":
			attType = "image"
		}
		parts := strings.SplitN(entry.Name(), "_", 3)
		name := entry.Name()
		if len(parts) == 3 {
			name = parts[2]
		}
		refs = append(refs, AttachmentRef{
			ID: id, Name: name, Type: attType, Size: info.Size(), Path: absPath,
		})
	}
	return refs, nil
}

// triggerHandoff initiates a handoff for a session.
// Uses HandoffHandler if configured, otherwise performs file-based handoff.
func (m *Manager) triggerHandoff(sessionID string) {
	var conversationID, lastAgent string
	err := m.db.QueryRow(`SELECT COALESCE(conversation_id, ''), COALESCE(last_agent, '') FROM sessions WHERE id = ?`, sessionID).Scan(&conversationID, &lastAgent)
	if err != nil {
		_, _ = m.db.Exec(`UPDATE sessions SET status = ? WHERE id = ?`, StatusIdle, sessionID)
		m.mu.Lock()
		delete(m.idleMap, sessionID)
		m.mu.Unlock()
		return
	}

	if m.config.HandoffHdl != nil {
		summary := m.readSummary(sessionID)
		if err := m.config.HandoffHdl.HandleHandoff(sessionID, summary.Summary, summary.Status, summary.Next); err != nil {
			log.Printf("handoff for session %s failed: %v", sessionID, err)
		}
	}

	_, _ = m.db.Exec(`UPDATE sessions SET status = ?, conversation_id = NULL, screenshot_count = 0 WHERE id = ?`, StatusIdle, sessionID)
	m.mu.Lock()
	delete(m.idleMap, sessionID)
	m.mu.Unlock()
}

// resetIdleTimer resets the idle timer for a session.
func (m *Manager) resetIdleTimer(sessionID string) {
	m.mu.Lock()
	entry, ok := m.idleMap[sessionID]
	if !ok {
		entry = &idleEntry{}
		m.idleMap[sessionID] = entry
	}
	m.mu.Unlock()

	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.timer != nil {
		entry.timer.Stop()
	}
	entry.timer = time.AfterFunc(m.config.IdleTimeout, func() {
		m.triggerHandoff(sessionID)
	})
}

// ProcessNextFromQueue processes the next queued item via Send.
func (m *Manager) ProcessNextFromQueue(sessionID string) {
	processQueueItem(m, sessionID)
}

// debounceSummary schedules summary generation (stub for Phase 2).
func (m *Manager) debounceSummary(sessionID string) {}

// generateSummary generates a summary (stub for Phase 2).
func (m *Manager) generateSummary(sessionID string) {}

// GetSessionStatus returns the current action status for a session.
func (m *Manager) GetSessionStatus(sessionID string) ActionStatus {
	m.mu.Lock()
	_, hasProcess := m.activeProcesses[sessionID]
	var actionCopy ActionStatus
	var hasAction bool
	if a, ok := m.lastActions[sessionID]; ok {
		actionCopy = *a
		hasAction = true
	}
	m.mu.Unlock()

	var title string
	_ = m.db.QueryRow(`SELECT COALESCE(title, '') FROM sessions WHERE id = ?`, sessionID).Scan(&title)
	if title == titleGenerating {
		title = ""
	}

	queueLen := m.QueueLength(sessionID)
	if hasProcess {
		if hasAction {
			actionCopy.SessionTitle = title
			actionCopy.QueueLength = queueLen
			return actionCopy
		}
		return ActionStatus{
			Status:       "active",
			SessionTitle: title,
			UpdatedAt:    nowUnix(),
			QueueLength:  queueLen,
		}
	}
	if hasAction {
		actionCopy.SessionTitle = title
		actionCopy.QueueLength = queueLen
		return actionCopy
	}
	return ActionStatus{
		Status:       "idle",
		SessionTitle: title,
		UpdatedAt:    nowUnix(),
		QueueLength:  queueLen,
	}
}

// Stubs for stop hook settings
var noStopHookSettingsPath string
var noStopHookSettingsOnce sync.Once
