package watcher

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ToolCall representa uma ferramenta invocada pelo modelo.
type ToolCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// Event representa um registro estruturado da transcrição (transcript.jsonl).
type Event struct {
	StepIndex  int        `json:"step_index"`
	Source     string     `json:"source"`
	Type       string     `json:"type"`
	Status     string     `json:"status"`
	CreatedAt  string     `json:"created_at"`
	Content    string     `json:"content"`
	Thinking   string     `json:"thinking"`
	ToolCalls  []ToolCall `json:"tool_calls"`
	IsQuestion bool       `json:"-"`
	IsDone     bool       `json:"-"`
}

// TranscriptInfo contém metadados de uma sessão descoberta no disco.
type TranscriptInfo struct {
	Path           string
	ConversationID string
	ModTime        time.Time
	Size           int64
}

// SessionSummary contém metadados para exibição e filtragem de sessões no dashboard e CLI.
type SessionSummary struct {
	ID                 string    `json:"id"`
	ShortID            string    `json:"short_id"`
	Workspace          string    `json:"workspace"`
	WorkspaceName      string    `json:"workspace_name"`
	FirstPrompt        string    `json:"first_prompt"`         // Prompt inicial completo (para tooltip/hover)
	FirstPromptSnippet string    `json:"first_prompt_snippet"` // Snippet conciso de uma linha para o dropdown
	ModTime            time.Time `json:"mod_time"`
	RelativeTime       string    `json:"relative_time"`
	IsActive           bool      `json:"is_active"`
	Path               string    `json:"path"`
}

// FindActiveTranscript localiza a sessão mais recente em ~/.gemini/brain, ~/.gemini/antigravity-cli/brain ou ~/.gemini/antigravity/brain.
func FindActiveTranscript(geminiDir string) (*TranscriptInfo, error) {
	candidates := []string{
		filepath.Join(geminiDir, "brain"),
		filepath.Join(geminiDir, "antigravity-cli", "brain"),
		filepath.Join(geminiDir, "antigravity", "brain"),
	}
	if strings.HasSuffix(geminiDir, "brain") {
		candidates = append([]string{geminiDir}, candidates...)
	}

	var latest *TranscriptInfo
	var searchedDirs []string

	for _, brainDir := range candidates {
		entries, err := os.ReadDir(brainDir)
		if err != nil {
			continue
		}
		searchedDirs = append(searchedDirs, brainDir)

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			candidate := filepath.Join(brainDir, entry.Name(), ".system_generated", "logs", "transcript.jsonl")
			info, err := os.Stat(candidate)
			if err == nil {
				if latest == nil || info.ModTime().After(latest.ModTime) {
					latest = &TranscriptInfo{
						Path:           candidate,
						ConversationID: entry.Name(),
						ModTime:        info.ModTime(),
						Size:           info.Size(),
					}
				}
			}
		}
	}

	if latest == nil {
		if len(searchedDirs) == 0 {
			return nil, fmt.Errorf("falha ao ler pasta brain em %v", candidates)
		}
		return nil, fmt.Errorf("nenhum transcript ativo encontrado em %v", searchedDirs)
	}

	return latest, nil
}

// FindSession localiza o transcript de uma sessão específica pelo conversationID, ou a mais recente se vazio.
func FindSession(geminiDir, convID string) (*TranscriptInfo, error) {
	if convID == "" {
		return FindActiveTranscript(geminiDir)
	}

	candidates := []string{
		filepath.Join(geminiDir, "brain", convID, ".system_generated", "logs", "transcript.jsonl"),
		filepath.Join(geminiDir, "antigravity-cli", "brain", convID, ".system_generated", "logs", "transcript.jsonl"),
		filepath.Join(geminiDir, "antigravity", "brain", convID, ".system_generated", "logs", "transcript.jsonl"),
	}
	if strings.HasSuffix(geminiDir, "brain") {
		candidates = append([]string{filepath.Join(geminiDir, convID, ".system_generated", "logs", "transcript.jsonl")}, candidates...)
	}

	for _, path := range candidates {
		info, err := os.Stat(path)
		if err == nil {
			return &TranscriptInfo{
				Path:           path,
				ConversationID: convID,
				ModTime:        info.ModTime(),
				Size:           info.Size(),
			}, nil
		}
	}

	// Fallback para prefix matching (ex: shortID de 8 caracteres)
	if len(convID) >= 4 {
		brainDirs := []string{
			filepath.Join(geminiDir, "brain"),
			filepath.Join(geminiDir, "antigravity-cli", "brain"),
			filepath.Join(geminiDir, "antigravity", "brain"),
		}
		if strings.HasSuffix(geminiDir, "brain") {
			brainDirs = append([]string{geminiDir}, brainDirs...)
		}
		for _, bDir := range brainDirs {
			entries, err := os.ReadDir(bDir)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				if entry.IsDir() && strings.HasPrefix(entry.Name(), convID) {
					fullID := entry.Name()
					path := filepath.Join(bDir, fullID, ".system_generated", "logs", "transcript.jsonl")
					info, err := os.Stat(path)
					if err == nil {
						return &TranscriptInfo{
							Path:           path,
							ConversationID: fullID,
							ModTime:        info.ModTime(),
							Size:           info.Size(),
						}, nil
					}
				}
			}
		}
	}

	return nil, fmt.Errorf("sessão %q não encontrada", convID)
}

// ListRecentSessions varre as pastas de transcrição e retorna resumos das sessões mais recentes.
func ListRecentSessions(geminiDir string, maxCount int) ([]SessionSummary, error) {
	if maxCount <= 0 {
		maxCount = 50
	}

	candidates := []string{
		filepath.Join(geminiDir, "brain"),
		filepath.Join(geminiDir, "antigravity-cli", "brain"),
		filepath.Join(geminiDir, "antigravity", "brain"),
	}
	if strings.HasSuffix(geminiDir, "brain") {
		candidates = append([]string{geminiDir}, candidates...)
	}

	type rawCandidate struct {
		path    string
		convID  string
		modTime time.Time
		size    int64
	}
	var rawList []rawCandidate
	seen := make(map[string]bool)

	for _, brainDir := range candidates {
		entries, err := os.ReadDir(brainDir)
		if err != nil {
			continue
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			convID := entry.Name()
			if seen[convID] {
				continue
			}

			path := filepath.Join(brainDir, convID, ".system_generated", "logs", "transcript.jsonl")
			info, err := os.Stat(path)
			if err != nil || info.Size() == 0 {
				continue
			}

			seen[convID] = true
			rawList = append(rawList, rawCandidate{
				path:    path,
				convID:  convID,
				modTime: info.ModTime(),
				size:    info.Size(),
			})
		}
	}

	// Ordena por modTime decrescente (sessões mais recentes primeiro)
	sort.Slice(rawList, func(i, j int) bool {
		return rawList[i].modTime.After(rawList[j].modTime)
	})

	if len(rawList) > maxCount {
		rawList = rawList[:maxCount]
	}

	var summaries []SessionSummary
	for _, c := range rawList {
		firstPrompt, snippet, ws := extractInitialPromptAndWorkspace(c.path)
		shortID := c.convID
		if len(shortID) > 8 {
			shortID = shortID[:8]
		}
		if snippet == "" {
			snippet = "Session " + shortID
		}
		wsName := ""
		if ws != "" {
			wsName = filepath.Base(ws)
		}

		isActive := time.Since(c.modTime) < 5*time.Minute

		summaries = append(summaries, SessionSummary{
			ID:                 c.convID,
			ShortID:            shortID,
			Workspace:          ws,
			WorkspaceName:      wsName,
			FirstPrompt:        firstPrompt,
			FirstPromptSnippet: snippet,
			ModTime:            c.modTime,
			RelativeTime:       formatRelativeTime(c.modTime),
			IsActive:           isActive,
			Path:               c.path,
		})
	}

	return summaries, nil
}

func extractInitialPromptAndWorkspace(transcriptPath string) (string, string, string) {
	file, err := os.Open(transcriptPath)
	if err != nil {
		return "", "", ""
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	var firstPrompt, snippet, workspace string

	lineCount := 0
	for scanner.Scan() {
		lineCount++
		if lineCount > 40 {
			break
		}
		line := scanner.Text()

		var evt struct {
			Type    string `json:"type"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal([]byte(line), &evt); err == nil && evt.Content != "" {
			if firstPrompt == "" && (evt.Type == "USER_INPUT" || strings.Contains(evt.Content, "<USER_REQUEST>")) {
				if strings.Contains(evt.Content, "<USER_REQUEST>") {
					parts := strings.Split(evt.Content, "<USER_REQUEST>")
					if len(parts) > 1 {
						sub := strings.Split(parts[1], "</USER_REQUEST>")
						firstPrompt = strings.TrimSpace(sub[0])
					}
				} else if evt.Type == "USER_INPUT" {
					firstPrompt = strings.TrimSpace(evt.Content)
				}
				if firstPrompt != "" {
					clean := strings.ReplaceAll(firstPrompt, "\n", " ")
					clean = strings.TrimSpace(clean)
					runes := []rune(clean)
					if len(runes) > 55 {
						snippet = string(runes[:52]) + "..."
					} else {
						snippet = string(runes)
					}
				}
			}

			if workspace == "" {
				if strings.Contains(evt.Content, "Active Workspaces:") {
					parts := strings.Split(evt.Content, "Active Workspaces:")
					if len(parts) > 1 {
						lines := strings.Split(parts[1], "\n")
						for _, l := range lines {
							l = strings.TrimSpace(l)
							if strings.HasPrefix(l, "- ") {
								ws := strings.Trim(strings.TrimPrefix(l, "- "), " \t\r\n\"'\\}{],")
								if ws != "" {
									workspace = ws
									break
								}
							}
						}
					}
				}
				if workspace == "" && strings.Contains(evt.Content, "Command Working Directory:") {
					parts := strings.Split(evt.Content, "Command Working Directory:")
					if len(parts) > 1 {
						lines := strings.Split(parts[1], "\n")
						if len(lines) > 0 {
							ws := strings.Trim(lines[0], " \t\r\n\"'\\}{],")
							if ws != "" {
								workspace = ws
							}
						}
					}
				}
			}
		}

		// Fallback for lines where json.Unmarshal didn't extract or workspace is in tool call JSON
		if firstPrompt == "" && strings.Contains(line, "<USER_REQUEST>") {
			parts := strings.Split(line, "<USER_REQUEST>")
			if len(parts) > 1 {
				sub := strings.Split(parts[1], "</USER_REQUEST>")
				raw := strings.TrimSpace(sub[0])
				raw = strings.ReplaceAll(raw, `\n`, "\n")
				raw = strings.ReplaceAll(raw, `\"`, `"`)
				firstPrompt = strings.TrimSpace(raw)
			}
			if firstPrompt != "" && snippet == "" {
				clean := strings.ReplaceAll(firstPrompt, "\n", " ")
				clean = strings.TrimSpace(clean)
				runes := []rune(clean)
				if len(runes) > 55 {
					snippet = string(runes[:52]) + "..."
				} else {
					snippet = string(runes)
				}
			}
		}

		if workspace == "" {
			if strings.Contains(line, "Active Workspaces:") {
				parts := strings.Split(line, "Active Workspaces:")
				if len(parts) > 1 {
					lines := strings.Split(parts[1], `\n`)
					for _, l := range lines {
						l = strings.TrimSpace(l)
						if strings.HasPrefix(l, "- ") {
							ws := strings.Trim(strings.TrimPrefix(l, "- "), " \t\r\n\"'\\}{],")
							if ws != "" {
								workspace = ws
								break
							}
						}
					}
				}
			}
			if workspace == "" && strings.Contains(line, "Command Working Directory:") {
				parts := strings.Split(line, "Command Working Directory:")
				if len(parts) > 1 {
					lines := strings.Split(parts[1], `\n`)
					if len(lines) > 0 {
						ws := strings.Trim(lines[0], " \t\r\n\"'\\}{],")
						if ws != "" {
							workspace = ws
						}
					}
				}
			}
			if workspace == "" && (strings.Contains(line, `"Cwd":`) || strings.Contains(line, `"cwd":`)) {
				idx := strings.Index(line, `"Cwd":`)
				if idx == -1 {
					idx = strings.Index(line, `"cwd":`)
				}
				if idx != -1 {
					sub := line[idx+len(`"Cwd":`):]
					sub = strings.TrimLeft(sub, ` "\'`)
					endIdx := strings.IndexAny(sub, `"\',`)
					if endIdx != -1 {
						ws := strings.Trim(sub[:endIdx], " \t\r\n\"'\\}{],")
						if strings.HasPrefix(ws, "/") {
							workspace = ws
						}
					}
				}
			}
		}

		if firstPrompt != "" && workspace != "" {
			break
		}
	}

	if snippet == "" && firstPrompt != "" {
		snippet = firstPrompt
	}

	return firstPrompt, snippet, workspace
}

func formatRelativeTime(t time.Time) string {
	diff := time.Since(t)
	if diff < 0 {
		diff = 0
	}
	if diff < time.Minute {
		return "Just now"
	}
	if diff < time.Hour {
		mins := int(diff.Minutes())
		return fmt.Sprintf("%dm ago", mins)
	}
	if diff < 24*time.Hour {
		hours := int(diff.Hours())
		return fmt.Sprintf("%dh ago", hours)
	}
	if diff < 48*time.Hour {
		return "Yesterday"
	}
	days := int(diff.Hours() / 24)
	if days < 7 {
		return fmt.Sprintf("%dd ago", days)
	}
	return t.Format("Jan 2")
}

// ParseLine decodifica uma linha JSONL em um Event estruturado.
func ParseLine(line []byte) (*Event, error) {
	clean := strings.TrimSpace(string(line))
	if len(clean) == 0 {
		return nil, fmt.Errorf("linha vazia")
	}

	var evt Event
	if err := json.Unmarshal([]byte(clean), &evt); err != nil {
		return nil, err
	}

	for _, call := range evt.ToolCalls {
		if call.Name == "ask_question" {
			evt.IsQuestion = true
		}
	}
	if evt.Status == "DONE" {
		evt.IsDone = true
	}

	return &evt, nil
}

// Summary extrai uma descrição compacta e legível do evento para exibição no terminal.
func (e *Event) Summary() string {
	var sb strings.Builder

	switch e.Type {
	case "USER_INPUT":
		content := strings.TrimSpace(e.Content)
		if len(content) > 100 {
			content = content[:97] + "..."
		}
		content = strings.ReplaceAll(content, "\n", " ")
		sb.WriteString(fmt.Sprintf("👤 [User #%d] %s", e.StepIndex, content))

	case "PLANNER_RESPONSE":
		if e.Thinking != "" {
			thinking := strings.TrimSpace(e.Thinking)
			if len(thinking) > 120 {
				thinking = thinking[:117] + "..."
			}
			thinking = strings.ReplaceAll(thinking, "\n", " ")
			sb.WriteString(fmt.Sprintf("💭 [Think #%d] %s\n", e.StepIndex, thinking))
		}
		if len(e.ToolCalls) > 0 {
			for i, tc := range e.ToolCalls {
				if i > 0 {
					sb.WriteString("\n")
				}
				if tc.Name == "ask_question" {
					sb.WriteString(fmt.Sprintf("🔔 [INTERAÇÃO #%d] O agente precisa da sua resposta!", e.StepIndex))
				} else if tc.Name == "invoke_subagent" {
					subs := parseInvokeSubagents(tc.Args)
					if len(subs) > 0 {
						var subStrs []string
						for _, sub := range subs {
							promptSummary := strings.TrimSpace(sub.Prompt)
							if len(promptSummary) > 80 {
								promptSummary = promptSummary[:77] + "..."
							}
							promptSummary = strings.ReplaceAll(promptSummary, "\n", " ")
							subStr := fmt.Sprintf("🤖 [SUBAGENT SPAWNED] Role: %q (type: %s)\n   ↳ Task: %q", sub.Role, sub.TypeName, promptSummary)
							subStrs = append(subStrs, subStr)
						}
						sb.WriteString(strings.Join(subStrs, "\n"))
					} else {
						argsSummary := extractArgsSummary(tc.Name, tc.Args)
						sb.WriteString(fmt.Sprintf("🛠️  [Tool #%d] %s(%s)", e.StepIndex, tc.Name, argsSummary))
					}
				} else if tc.Name == "send_message" {
					msgParam := parseSendMessage(tc.Args)
					if msgParam.Recipient != "" || msgParam.Message != "" {
						msgSummary := strings.TrimSpace(msgParam.Message)
						if len(msgSummary) > 80 {
							msgSummary = msgSummary[:77] + "..."
						}
						msgSummary = strings.ReplaceAll(msgSummary, "\n", " ")
						sb.WriteString(fmt.Sprintf("💬 [AGENT MESSAGE] To: %s | %s", msgParam.Recipient, msgSummary))
					} else {
						argsSummary := extractArgsSummary(tc.Name, tc.Args)
						sb.WriteString(fmt.Sprintf("🛠️  [Tool #%d] %s(%s)", e.StepIndex, tc.Name, argsSummary))
					}
				} else {
					argsSummary := extractArgsSummary(tc.Name, tc.Args)
					sb.WriteString(fmt.Sprintf("🛠️  [Tool #%d] %s(%s)", e.StepIndex, tc.Name, argsSummary))
				}
			}
		} else if e.Thinking == "" {
			sb.WriteString(fmt.Sprintf("🤖 [Agent #%d] Planejando próxima ação...", e.StepIndex))
		}

	case "GENERIC":
		content := strings.TrimSpace(e.Content)
		if len(content) > 80 {
			content = content[:77] + "..."
		}
		content = strings.ReplaceAll(content, "\n", " ")
		if content != "" {
			sb.WriteString(fmt.Sprintf("⚡ [Step #%d] %s", e.StepIndex, content))
		}

	default:
		sb.WriteString(fmt.Sprintf("ℹ️  [%s #%d] Status: %s", e.Type, e.StepIndex, e.Status))
	}

	return sb.String()
}

func extractArgsSummary(toolName string, rawArgs json.RawMessage) string {
	if len(rawArgs) == 0 {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal(rawArgs, &m); err != nil {
		return ""
	}

	switch toolName {
	case "run_command":
		if cmd, ok := m["CommandLine"].(string); ok {
			cmd = strings.TrimSpace(cmd)
			cmd = strings.Trim(cmd, `"`)
			if len(cmd) > 60 {
				return cmd[:57] + "..."
			}
			return cmd
		}
	case "view_file", "replace_file_content":
		if file, ok := m["AbsolutePath"].(string); ok {
			return filepath.Base(strings.Trim(file, `"`))
		}
		if file, ok := m["TargetFile"].(string); ok {
			return filepath.Base(strings.Trim(file, `"`))
		}
	case "write_to_file":
		if file, ok := m["TargetFile"].(string); ok {
			return filepath.Base(strings.Trim(file, `"`))
		}
	case "call_mcp_tool":
		if server, ok := m["ServerName"].(string); ok {
			if tool, ok := m["ToolName"].(string); ok {
				return fmt.Sprintf("%s/%s", server, tool)
			}
		}
	case "invoke_subagent":
		subs := parseInvokeSubagents(rawArgs)
		if len(subs) > 0 {
			return fmt.Sprintf("Role: %s (type: %s)", subs[0].Role, subs[0].TypeName)
		}
	case "send_message":
		msg := parseSendMessage(rawArgs)
		if msg.Recipient != "" {
			return fmt.Sprintf("To: %s", msg.Recipient)
		}
	}

	// Resumo padrão se não mapeado
	for _, v := range m {
		if s, ok := v.(string); ok && len(s) > 0 && len(s) < 40 {
			return s
		}
	}
	return ""
}

// SubagentNode representa um subagente invocado na árvore de execução.
type SubagentNode struct {
	Role      string         `json:"role"`
	TypeName  string         `json:"type_name"`
	Prompt    string         `json:"prompt"`
	Model     string         `json:"model,omitempty"`
	Workspace string         `json:"workspace,omitempty"`
	Children  []SubagentNode `json:"children,omitempty"`
	Messages  []AgentMessage `json:"messages,omitempty"`
}

// AgentMessage representa uma mensagem trocada entre agentes (send_message).
type AgentMessage struct {
	From      string `json:"from,omitempty"`
	To        string `json:"to"`
	Message   string `json:"message"`
	Timestamp string `json:"timestamp,omitempty"`
}

// SubagentTree mantém a hierarquia e o estado em memória dos subagentes e mensagens.
type SubagentTree struct {
	mu       sync.RWMutex
	Agents   []SubagentNode `json:"agents"`
	Messages []AgentMessage `json:"messages"`
}

// NewSubagentTree instancia uma nova árvore de subagentes.
func NewSubagentTree() *SubagentTree {
	return &SubagentTree{
		Agents:   make([]SubagentNode, 0),
		Messages: make([]AgentMessage, 0),
	}
}

var globalSubagentTree = NewSubagentTree()

// GetGlobalSubagentTree retorna a árvore global compartilhada de subagentes.
func GetGlobalSubagentTree() *SubagentTree {
	return globalSubagentTree
}

// AddSpawn adiciona um subagente invocado à árvore.
func (t *SubagentTree) AddSpawn(role, typeName, prompt, model, workspace string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Agents = append(t.Agents, SubagentNode{
		Role:      role,
		TypeName:  typeName,
		Prompt:    prompt,
		Model:     model,
		Workspace: workspace,
		Children:  make([]SubagentNode, 0),
		Messages:  make([]AgentMessage, 0),
	})
}

// AddMessage adiciona uma mensagem entre agentes à árvore.
func (t *SubagentTree) AddMessage(to, message string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	msg := AgentMessage{
		To:      to,
		Message: message,
	}
	t.Messages = append(t.Messages, msg)
	if len(t.Agents) > 0 {
		lastAgent := &t.Agents[len(t.Agents)-1]
		lastAgent.Messages = append(lastAgent.Messages, msg)
	}
}

// GetActiveSubagents retorna uma cópia dos subagentes ativos de forma thread-safe.
func (t *SubagentTree) GetActiveSubagents() []SubagentNode {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]SubagentNode, len(t.Agents))
	copy(out, t.Agents)
	return out
}

// FormatTree formata visualmente a árvore de subagentes e mensagens.
func (t *SubagentTree) FormatTree() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	var sb strings.Builder
	sb.WriteString("=== Active Subagents Tree ===\n")
	if len(t.Agents) == 0 {
		sb.WriteString("  (no active subagents)\n")
	}
	for i, agent := range t.Agents {
		sb.WriteString(fmt.Sprintf("%d. Role: %q (type: %s)\n", i+1, agent.Role, agent.TypeName))
		if agent.Prompt != "" {
			p := strings.TrimSpace(agent.Prompt)
			if len(p) > 80 {
				p = p[:77] + "..."
			}
			p = strings.ReplaceAll(p, "\n", " ")
			sb.WriteString(fmt.Sprintf("   ↳ Task: %q\n", p))
		}
		if len(agent.Messages) > 0 {
			sb.WriteString("   ↳ Messages:\n")
			for _, m := range agent.Messages {
				msgSummary := strings.TrimSpace(m.Message)
				if len(msgSummary) > 60 {
					msgSummary = msgSummary[:57] + "..."
				}
				msgSummary = strings.ReplaceAll(msgSummary, "\n", " ")
				sb.WriteString(fmt.Sprintf("     - To: %s | %s\n", m.To, msgSummary))
			}
		}
	}
	if len(t.Messages) > 0 {
		sb.WriteString("=== Inter-Agent Messages ===\n")
		for _, m := range t.Messages {
			msgSummary := strings.TrimSpace(m.Message)
			if len(msgSummary) > 60 {
				msgSummary = msgSummary[:57] + "..."
			}
			msgSummary = strings.ReplaceAll(msgSummary, "\n", " ")
			sb.WriteString(fmt.Sprintf("💬 To: %s | %s\n", m.To, msgSummary))
		}
	}
	return sb.String()
}

// ProcessEvent processa um evento e atualiza a árvore de subagentes se houver invoke_subagent ou send_message.
func (t *SubagentTree) ProcessEvent(e *Event) {
	if e == nil {
		return
	}
	for _, tc := range e.ToolCalls {
		switch tc.Name {
		case "invoke_subagent":
			subs := parseInvokeSubagents(tc.Args)
			for _, sub := range subs {
				t.AddSpawn(sub.Role, sub.TypeName, sub.Prompt, "", "")
			}
		case "send_message":
			msg := parseSendMessage(tc.Args)
			if msg.Recipient != "" || msg.Message != "" {
				t.AddMessage(msg.Recipient, msg.Message)
			}
		}
	}
}

type SubagentParam struct {
	Role     string
	TypeName string
	Prompt   string
}

func parseInvokeSubagents(rawArgs json.RawMessage) []SubagentParam {
	var result []SubagentParam
	if len(rawArgs) == 0 {
		return result
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(rawArgs, &raw); err != nil {
		return result
	}

	subagentsVal, ok := raw["Subagents"]
	if !ok {
		subagentsVal, ok = raw["subagents"]
	}

	if ok {
		if list, ok := subagentsVal.([]interface{}); ok {
			for _, item := range list {
				if m, ok := item.(map[string]interface{}); ok {
					role := getStringField(m, "Role", "role")
					typeName := getStringField(m, "TypeName", "typename", "Type", "type")
					prompt := getStringField(m, "Prompt", "prompt", "Goal", "goal")
					result = append(result, SubagentParam{
						Role:     role,
						TypeName: typeName,
						Prompt:   prompt,
					})
				}
			}
		}
	} else {
		role := getStringField(raw, "Role", "role")
		typeName := getStringField(raw, "TypeName", "typename", "Type", "type")
		prompt := getStringField(raw, "Prompt", "prompt", "Goal", "goal")
		if role != "" || typeName != "" || prompt != "" {
			result = append(result, SubagentParam{
				Role:     role,
				TypeName: typeName,
				Prompt:   prompt,
			})
		}
	}

	return result
}

type SendMessageParam struct {
	Recipient string
	Message   string
}

func parseSendMessage(rawArgs json.RawMessage) SendMessageParam {
	var p SendMessageParam
	if len(rawArgs) == 0 {
		return p
	}
	var m map[string]interface{}
	if err := json.Unmarshal(rawArgs, &m); err != nil {
		return p
	}
	p.Recipient = getStringField(m, "Recipient", "recipient", "RecipientID", "recipient_id")
	p.Message = getStringField(m, "Message", "message", "Content", "content")
	return p
}

func getStringField(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if val, ok := m[k]; ok {
			if s, ok := val.(string); ok {
				return s
			}
		}
	}
	return ""
}

// NotifyExecutor permite interceptar comandos de notificação no SO (útil para testes ou mocks).
var NotifyExecutor = func(osName, title, message string) {
	switch osName {
	case "darwin":
		script := fmt.Sprintf(`display notification %q with title %q`, message, title)
		_ = exec.Command("osascript", "-e", script).Run()
	case "linux":
		_ = exec.Command("notify-send", title, message).Run()
	}
}

// Notify emite notificação visual no SO (macOS/Linux) e alerta sonoro no terminal.
func Notify(osName, title, message string) {
	if os.Getenv("AGYO_DISABLE_NOTIFY") == "1" {
		return
	}
	// Alerta sonoro de terminal (bell)
	fmt.Print("\a")

	NotifyExecutor(osName, title, message)
}

// WatchOptions configura o comportamento do tailing.
type WatchOptions struct {
	Follow       bool
	NotifyOnWait bool
	PollInterval time.Duration
	InitialSteps int
	OSName       string
}

// Stream acompanha o arquivo transcript.jsonl e envia eventos para o handler.
func Stream(ctx context.Context, transcriptPath string, opts WatchOptions, handler func(*Event)) error {
	file, err := os.Open(transcriptPath)
	if err != nil {
		return fmt.Errorf("falha ao abrir transcript: %w", err)
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	var allEvents []*Event
	var partial []byte

	// 1. Fase Inicial: lê tudo o que já existe
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			if len(partial) > 0 {
				line = append(partial, line...)
				partial = nil
			}
			if line[len(line)-1] == '\n' {
				if evt, pErr := ParseLine(line); pErr == nil {
					allEvents = append(allEvents, evt)
					globalSubagentTree.ProcessEvent(evt)
				}
			} else {
				partial = append(partial, line...)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}

	// Emite os passos iniciais solicitados (ex: últimos N passos)
	startIdx := 0
	if opts.InitialSteps > 0 && len(allEvents) > opts.InitialSteps {
		startIdx = len(allEvents) - opts.InitialSteps
	}
	for i := startIdx; i < len(allEvents); i++ {
		handler(allEvents[i])
	}

	if !opts.Follow {
		return nil
	}

	poll := opts.PollInterval
	if poll <= 0 {
		poll = 400 * time.Millisecond
	}

	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	// 2. Fase de Tailing: escuta novos dados continuamente
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			for {
				line, err := reader.ReadBytes('\n')
				if len(line) > 0 {
					if len(partial) > 0 {
						line = append(partial, line...)
						partial = nil
					}
					if line[len(line)-1] == '\n' {
						if evt, pErr := ParseLine(line); pErr == nil {
							globalSubagentTree.ProcessEvent(evt)
							handler(evt)
							if opts.NotifyOnWait && evt.IsQuestion {
								Notify(opts.OSName, "Antigravity Operator", "O agente precisa da sua resposta (pergunta interativa)!")
							}
						}
					} else {
						partial = append(partial, line...)
					}
				}
				if err == io.EOF {
					break
				}
				if err != nil {
					return err
				}
			}
		}
	}
}
