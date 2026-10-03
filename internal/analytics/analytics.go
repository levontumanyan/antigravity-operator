package analytics

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tiagoboas/antigravity-operator/internal/watcher"
)

// CommandStat stores execution frequency for a shell command.
type CommandStat struct {
	Command string `json:"command"`
	Count   int    `json:"count"`
}

// RepetitionAlert represents a warning when a tool or command is invoked repetitively.
type RepetitionAlert struct {
	Type    string `json:"type"`    // "command_loop", "polling_loop", "file_reread"
	Target  string `json:"target"`  // command line, task id, or file path
	Count   int    `json:"count"`   // number of occurrences
	Message string `json:"message"` // human-readable explanation
}

// ToolCallRecord captures full execution context for a single tool call.
type ToolCallRecord struct {
	ID                int     `json:"id"`
	StepIndex         int     `json:"step_index"`
	Tool              string  `json:"tool"`
	Command           string  `json:"command,omitempty"`
	TargetFile        string  `json:"target_file,omitempty"`
	Summary           string  `json:"summary"`
	IsAutonomous      bool    `json:"is_autonomous"`       // Executed autonomously in an agent loop (no user prompt or confirmation)
	IsPromptTriggered bool    `json:"is_prompt_triggered"` // Executed immediately after a user prompt
	RequiresApproval  bool    `json:"requires_approval"`   // Prompted to the user for interactive terminal confirmation
	IsUserApproved    bool    `json:"is_user_approved"`    // Explicitly approved by user in terminal prompt
	UserConfirmation  string  `json:"user_confirmation,omitempty"` // "approved" or "rejected"
	IsFailed          bool    `json:"is_failed"`           // Exited with error or non-zero status
	FailureReason     string  `json:"failure_reason,omitempty"`
	DurationSeconds   float64 `json:"duration_seconds,omitempty"`
	Timestamp         string  `json:"timestamp"`
	UserPromptContext string  `json:"user_prompt_context,omitempty"`
}

// SessionAnalytics consolidates tool calls, command patterns, and efficiency metrics.
type SessionAnalytics struct {
	ConversationID      string            `json:"conversation_id"`
	Workspace           string            `json:"workspace,omitempty"`
	TranscriptPath      string            `json:"transcript_path"`
	TotalSteps          int               `json:"total_steps"`
	UserTurns           int               `json:"user_turns"`
	ModelTurns          int               `json:"model_turns"`
	SystemMessages      int               `json:"system_messages"`
	TotalToolCalls      int               `json:"total_tool_calls"`
	AutonomousToolCalls int               `json:"autonomous_tool_calls"`
	ApprovedToolCalls   int               `json:"approved_tool_calls"`
	PromptedToolCalls   int               `json:"prompted_tool_calls"`
	FailedToolCalls     int               `json:"failed_tool_calls"`
	ToolCounts          map[string]int    `json:"tool_counts"`
	TopCommands         []CommandStat     `json:"top_commands"`
	FilesRead           map[string]int    `json:"files_read"`
	FilesModified       map[string]int    `json:"files_modified"`
	Repetitions         []RepetitionAlert `json:"repetitions"`
	ToolCalls           []ToolCallRecord  `json:"tool_calls"`
	StartTime           time.Time         `json:"start_time"`
	LastActiveTime      time.Time         `json:"last_active_time"`
	DurationSeconds     float64           `json:"duration_seconds"`
}

// AnalyzeTranscript reads a transcript.jsonl file and computes analytics and efficiency alerts.
func AnalyzeTranscript(transcriptPath string) (*SessionAnalytics, error) {
	file, err := os.Open(transcriptPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open transcript: %w", err)
	}
	defer file.Close()

	convID := extractConversationID(transcriptPath)
	confirmations := LoadToolConfirmations(convID)

	sa := &SessionAnalytics{
		ConversationID: convID,
		TranscriptPath: transcriptPath,
		ToolCounts:     make(map[string]int),
		FilesRead:      make(map[string]int),
		FilesModified:  make(map[string]int),
		Repetitions:    make([]RepetitionAlert, 0),
		ToolCalls:      make([]ToolCallRecord, 0),
	}

	commandFreq := make(map[string]int)
	taskPollingFreq := make(map[string]int)

	scanner := bufio.NewScanner(file)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

	var lastCommand string
	var consecutiveCommandCount int
	var currentPrompt string
	var nextPlannerIsPromptTriggered bool
	var pendingToolIndices []int
	toolCallCounter := 0

	for scanner.Scan() {
		line := scanner.Bytes()
		evt, err := watcher.ParseLine(line)
		if err != nil {
			continue
		}

		sa.TotalSteps++

		// Track timestamps
		var stepTime time.Time
		if evt.CreatedAt != "" {
			t, err := time.Parse(time.RFC3339, evt.CreatedAt)
			if err == nil {
				stepTime = t
				if sa.StartTime.IsZero() || t.Before(sa.StartTime) {
					sa.StartTime = t
				}
				if t.After(sa.LastActiveTime) {
					sa.LastActiveTime = t
				}
			}
		}

		switch evt.Type {
		case "USER_INPUT":
			sa.UserTurns++
			lastCommand = ""
			consecutiveCommandCount = 0
			pendingToolIndices = nil // Reset pending indices so cancelled/unanswered calls don't shift subsequent results
			currentPrompt = extractCleanPrompt(evt.Content)
			nextPlannerIsPromptTriggered = true

			if sa.Workspace == "" {
				if strings.Contains(evt.Content, "Active Workspaces:") {
					parts := strings.Split(evt.Content, "Active Workspaces:")
					if len(parts) > 1 {
						for _, l := range strings.Split(parts[1], "\n") {
							l = strings.TrimSpace(l)
							if strings.HasPrefix(l, "- ") {
								sa.Workspace = strings.TrimSpace(strings.TrimPrefix(l, "- "))
								break
							}
						}
					}
				}
				if sa.Workspace == "" && strings.Contains(evt.Content, "Command Working Directory:") {
					parts := strings.Split(evt.Content, "Command Working Directory:")
					if len(parts) > 1 {
						lines := strings.Split(parts[1], "\n")
						if len(lines) > 0 {
							sa.Workspace = strings.TrimSpace(lines[0])
						}
					}
				}
			}

		case "PLANNER_RESPONSE":
			sa.ModelTurns++

			isPromptTurn := nextPlannerIsPromptTriggered
			nextPlannerIsPromptTriggered = false

			for _, call := range evt.ToolCalls {
				toolCallCounter++
				sa.TotalToolCalls++
				sa.ToolCounts[call.Name]++

				if call.Name != "run_command" {
					lastCommand = ""
					consecutiveCommandCount = 0
				}

				record := ToolCallRecord{
					ID:                toolCallCounter,
					StepIndex:         evt.StepIndex,
					Tool:              call.Name,
					IsPromptTriggered: isPromptTurn,
					IsAutonomous:      !isPromptTurn,
					Timestamp:         evt.CreatedAt,
					UserPromptContext: currentPrompt,
				}

				if isPromptTurn {
					sa.PromptedToolCalls++
				} else {
					sa.AutonomousToolCalls++
				}

				switch call.Name {
				case "run_command":
					var args struct {
						CommandLine string `json:"CommandLine"`
						ToolSummary string `json:"toolSummary"`
						Cwd         string `json:"Cwd"`
					}
					if err := json.Unmarshal(call.Args, &args); err == nil && args.CommandLine != "" {
						cmd := strings.Trim(strings.TrimSpace(args.CommandLine), "\"'")
						record.Command = cmd
						if sa.Workspace == "" && args.Cwd != "" {
							cleanCwd := strings.Trim(strings.TrimSpace(args.Cwd), "\"'")
							if cleanCwd != "" {
								sa.Workspace = cleanCwd
							}
						}
						if args.ToolSummary != "" {
							record.Summary = args.ToolSummary
						} else {
							record.Summary = cmd
						}
						commandFreq[cmd]++

						if cmd == lastCommand {
							consecutiveCommandCount++
							if consecutiveCommandCount >= 3 {
								msg := fmt.Sprintf("Consecutive command loop: %q executed %d times in a row", cmd, consecutiveCommandCount)
								updated := false
								for i := range sa.Repetitions {
									if sa.Repetitions[i].Type == "command_loop" && sa.Repetitions[i].Target == cmd {
										sa.Repetitions[i].Count = consecutiveCommandCount
										sa.Repetitions[i].Message = msg
										updated = true
										break
									}
								}
								if !updated {
									sa.Repetitions = append(sa.Repetitions, RepetitionAlert{
										Type:    "command_loop",
										Target:  cmd,
										Count:   consecutiveCommandCount,
										Message: msg,
									})
								}
							}
						} else {
							lastCommand = cmd
							consecutiveCommandCount = 1
						}
					}

				case "view_file":
					var args struct {
						AbsolutePath string `json:"AbsolutePath"`
						ToolSummary  string `json:"toolSummary"`
					}
					if err := json.Unmarshal(call.Args, &args); err == nil && args.AbsolutePath != "" {
						cleanPath := strings.Trim(strings.TrimSpace(args.AbsolutePath), "\"'")
						record.TargetFile = cleanPath
						if sa.Workspace == "" && cleanPath != "" && strings.HasPrefix(cleanPath, "/") {
							for d := filepath.Dir(cleanPath); d != "/" && d != "." && len(d) > 1; d = filepath.Dir(d) {
								if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
									sa.Workspace = d
									break
								}
							}
						}
						if args.ToolSummary != "" {
							record.Summary = args.ToolSummary
						} else {
							record.Summary = "Read " + cleanPath
						}
						sa.FilesRead[cleanPath]++
					}

				case "replace_file_content", "write_to_file":
					var args struct {
						TargetFile  string `json:"TargetFile"`
						Description string `json:"Description"`
						ToolSummary string `json:"toolSummary"`
					}
					if err := json.Unmarshal(call.Args, &args); err == nil && args.TargetFile != "" {
						cleanTarget := strings.Trim(strings.TrimSpace(args.TargetFile), "\"'")
						record.TargetFile = cleanTarget
						if args.ToolSummary != "" {
							record.Summary = args.ToolSummary
						} else if args.Description != "" {
							record.Summary = args.Description
						} else {
							record.Summary = "Edit " + cleanTarget
						}
						sa.FilesModified[cleanTarget]++
					}

				case "manage_task":
					var args struct {
						Action      string `json:"Action"`
						TaskId      string `json:"TaskId"`
						ToolSummary string `json:"toolSummary"`
					}
					if err := json.Unmarshal(call.Args, &args); err == nil {
						record.Summary = fmt.Sprintf("Task %s (%s)", args.Action, args.TaskId)
						if args.Action == "status" && args.TaskId != "" {
							taskPollingFreq[args.TaskId]++
						}
					}

				case "schedule":
					var args struct {
						DurationSeconds int    `json:"DurationSeconds"`
						Prompt          string `json:"Prompt"`
						TimerCondition  string `json:"TimerCondition"`
					}
					if err := json.Unmarshal(call.Args, &args); err == nil {
						record.Summary = fmt.Sprintf("Timer (%ds: %s)", args.DurationSeconds, args.Prompt)
					}

				default:
					record.Summary = call.Name
				}

				recIndex := len(sa.ToolCalls)
				sa.ToolCalls = append(sa.ToolCalls, record)
				pendingToolIndices = append(pendingToolIndices, recIndex)
			}

		case "GENERIC":
			// Tool execution result
			if len(pendingToolIndices) > 0 {
				idx := pendingToolIndices[0]
				pendingToolIndices = pendingToolIndices[1:]

				// Check if this execution required manual user confirmation in the terminal
				stepIdx := evt.StepIndex
				toolStep := sa.ToolCalls[idx].StepIndex
				approved, isConfirmed := confirmations[stepIdx]
				if !isConfirmed {
					approved, isConfirmed = confirmations[toolStep]
				}

				if isConfirmed {
					sa.ToolCalls[idx].RequiresApproval = true
					sa.ToolCalls[idx].IsUserApproved = approved
					if approved {
						sa.ToolCalls[idx].UserConfirmation = "approved"
					} else {
						sa.ToolCalls[idx].UserConfirmation = "rejected"
					}
					// A command that required manual confirmation is NOT autonomous
					if sa.ToolCalls[idx].IsAutonomous {
						sa.ToolCalls[idx].IsAutonomous = false
						if sa.AutonomousToolCalls > 0 {
							sa.AutonomousToolCalls--
						}
					} else if sa.ToolCalls[idx].IsPromptTriggered {
						if sa.PromptedToolCalls > 0 {
							sa.PromptedToolCalls--
						}
					}
					sa.ApprovedToolCalls++
				}

				content := evt.Content
				isFailed := false
				var failureMsg string

				if evt.Status == "ERROR" {
					isFailed = true
					failureMsg = "Tool reported error status"
				} else if strings.Contains(content, "Encountered error in tool execution") {
					isFailed = true
					for _, l := range strings.Split(content, "\n") {
						if strings.Contains(l, "Encountered error in tool execution") {
							failureMsg = strings.TrimSpace(l)
							break
						}
					}
				} else {
					for _, l := range strings.Split(content, "\n") {
						trimmed := strings.TrimSpace(l)
						if strings.HasPrefix(trimmed, "The command exited with code ") {
							codeStr := strings.TrimSuffix(strings.TrimPrefix(trimmed, "The command exited with code "), ".")
							if codeStr != "0" {
								isFailed = true
								failureMsg = trimmed
							}
						}
					}
				}

				if isFailed {
					sa.ToolCalls[idx].IsFailed = true
					sa.ToolCalls[idx].FailureReason = failureMsg
					sa.FailedToolCalls++
				}

				if !stepTime.IsZero() && sa.ToolCalls[idx].Timestamp != "" {
					tStart, err := time.Parse(time.RFC3339, sa.ToolCalls[idx].Timestamp)
					if err == nil && stepTime.After(tStart) {
						sa.ToolCalls[idx].DurationSeconds = stepTime.Sub(tStart).Seconds()
					}
				}
			}

		case "SYSTEM_MESSAGE":
			sa.SystemMessages++
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read transcript: %w", err)
	}

	if !sa.StartTime.IsZero() && !sa.LastActiveTime.IsZero() {
		sa.DurationSeconds = sa.LastActiveTime.Sub(sa.StartTime).Seconds()
	}

	// Calculate top commands (sorted keys for deterministic iteration)
	cmdKeys := make([]string, 0, len(commandFreq))
	for cmd := range commandFreq {
		cmdKeys = append(cmdKeys, cmd)
	}
	sort.Strings(cmdKeys)

	for _, cmd := range cmdKeys {
		count := commandFreq[cmd]
		sa.TopCommands = append(sa.TopCommands, CommandStat{Command: cmd, Count: count})
		if count >= 3 {
			found := false
			for i := range sa.Repetitions {
				if sa.Repetitions[i].Type == "command_loop" && sa.Repetitions[i].Target == cmd {
					found = true
					if count > sa.Repetitions[i].Count {
						consecutiveCount := sa.Repetitions[i].Count
						sa.Repetitions[i].Count = count
						sa.Repetitions[i].Message = fmt.Sprintf("Repetitive command: %q executed %d times across session (%d in a row)", cmd, count, consecutiveCount)
					}
					break
				}
			}
			if !found {
				sa.Repetitions = append(sa.Repetitions, RepetitionAlert{
					Type:    "command_loop",
					Target:  cmd,
					Count:   count,
					Message: fmt.Sprintf("Repetitive command: %q executed %d times across session", cmd, count),
				})
			}
		}
	}
	sort.Slice(sa.TopCommands, func(i, j int) bool {
		if sa.TopCommands[i].Count != sa.TopCommands[j].Count {
			return sa.TopCommands[i].Count > sa.TopCommands[j].Count
		}
		return sa.TopCommands[i].Command < sa.TopCommands[j].Command
	})
	if len(sa.TopCommands) > 10 {
		sa.TopCommands = sa.TopCommands[:10]
	}

	// Flag task polling loops (sorted keys)
	taskKeys := make([]string, 0, len(taskPollingFreq))
	for taskID := range taskPollingFreq {
		taskKeys = append(taskKeys, taskID)
	}
	sort.Strings(taskKeys)

	for _, taskID := range taskKeys {
		count := taskPollingFreq[taskID]
		if count >= 3 {
			sa.Repetitions = append(sa.Repetitions, RepetitionAlert{
				Type:    "polling_loop",
				Target:  taskID,
				Count:   count,
				Message: fmt.Sprintf("Polling loop: status of %q checked %d times", taskID, count),
			})
		}
	}

	// Flag redundant file reads (sorted keys)
	fileKeys := make([]string, 0, len(sa.FilesRead))
	for file := range sa.FilesRead {
		fileKeys = append(fileKeys, file)
	}
	sort.Strings(fileKeys)

	for _, file := range fileKeys {
		count := sa.FilesRead[file]
		if count >= 4 {
			sa.Repetitions = append(sa.Repetitions, RepetitionAlert{
				Type:    "file_reread",
				Target:  file,
				Count:   count,
				Message: fmt.Sprintf("Frequent file re-read: %s inspected %d times", file, count),
			})
		}
	}

	// Sort Repetitions deterministically: highest count first, then by type, then by target
	sort.Slice(sa.Repetitions, func(i, j int) bool {
		if sa.Repetitions[i].Count != sa.Repetitions[j].Count {
			return sa.Repetitions[i].Count > sa.Repetitions[j].Count
		}
		if sa.Repetitions[i].Type != sa.Repetitions[j].Type {
			return sa.Repetitions[i].Type < sa.Repetitions[j].Type
		}
		return sa.Repetitions[i].Target < sa.Repetitions[j].Target
	})

	return sa, nil
}

func extractCleanPrompt(content string) string {
	content = strings.TrimSpace(content)
	if strings.Contains(content, "<USER_REQUEST>") {
		parts := strings.Split(content, "<USER_REQUEST>")
		if len(parts) > 1 {
			sub := strings.Split(parts[1], "</USER_REQUEST>")
			content = strings.TrimSpace(sub[0])
		}
	}
	content = strings.ReplaceAll(content, "\n", " ")
	runes := []rune(content)
	if len(runes) > 60 {
		return string(runes[:57]) + "..."
	}
	return string(runes)
}

func hasRepetitionTarget(alerts []RepetitionAlert, target string) bool {
	for _, a := range alerts {
		if a.Target == target {
			return true
		}
	}
	return false
}

// FilterOptions allows filtering tool calls for CLI and reporting.
type FilterOptions struct {
	AutonomousOnly bool
	ApprovedOnly   bool
	PromptedOnly   bool
	FailedOnly     bool
	ToolFilter     string
}

// FilterToolCalls returns a filtered slice of tool call records.
func (sa *SessionAnalytics) FilterToolCalls(opts FilterOptions) []ToolCallRecord {
	var filtered []ToolCallRecord
	for _, c := range sa.ToolCalls {
		if opts.AutonomousOnly && !c.IsAutonomous {
			continue
		}
		if opts.ApprovedOnly && !c.IsUserApproved {
			continue
		}
		if opts.PromptedOnly && !c.IsPromptTriggered {
			continue
		}
		if opts.FailedOnly && !c.IsFailed {
			continue
		}
		if opts.ToolFilter != "" && !strings.EqualFold(c.Tool, opts.ToolFilter) {
			continue
		}
		filtered = append(filtered, c)
	}
	return filtered
}

// FormatCLI renders an executive summary of the session analytics for terminal viewing.
func (sa *SessionAnalytics) FormatCLI(opts ...FilterOptions) string {
	var sb strings.Builder

	sb.WriteString("📊 Antigravity Session Tool Analytics & Efficiency\n")
	sb.WriteString("-----------------------------------------------------------------\n")
	if sa.ConversationID != "" {
		sb.WriteString(fmt.Sprintf("🆔 Session ID     : %s\n", sa.ConversationID))
	}
	sb.WriteString(fmt.Sprintf("⚡ Total Steps    : %d (Model: %d, User: %d, System: %d)\n",
		sa.TotalSteps, sa.ModelTurns, sa.UserTurns, sa.SystemMessages))

	autoPct := 0
	approvedPct := 0
	promptPct := 0
	if sa.TotalToolCalls > 0 {
		autoPct = (sa.AutonomousToolCalls * 100) / sa.TotalToolCalls
		approvedPct = (sa.ApprovedToolCalls * 100) / sa.TotalToolCalls
		promptPct = (sa.PromptedToolCalls * 100) / sa.TotalToolCalls
	}
	sb.WriteString(fmt.Sprintf("🛠️  Total Tool Calls: %d [⚡ Autonomous: %d (%d%%) | ✋ User Approved: %d (%d%%) | 👤 User Prompted: %d (%d%%)]\n",
		sa.TotalToolCalls, sa.AutonomousToolCalls, autoPct, sa.ApprovedToolCalls, approvedPct, sa.PromptedToolCalls, promptPct))

	if sa.FailedToolCalls > 0 {
		failPct := (sa.FailedToolCalls * 100) / sa.TotalToolCalls
		sb.WriteString(fmt.Sprintf("❌ Failed Calls   : %d (%d%%)\n", sa.FailedToolCalls, failPct))
	} else {
		sb.WriteString("✅ Failed Calls   : 0 (100% clean exit codes)\n")
	}

	if sa.DurationSeconds > 0 {
		d := time.Duration(sa.DurationSeconds) * time.Second
		sb.WriteString(fmt.Sprintf("⏱️  Duration       : %s\n", d.Round(time.Second)))
	}

	sb.WriteString("\n📈 Tool Call Distribution:\n")
	if len(sa.ToolCounts) == 0 {
		sb.WriteString("   (no tool calls in this session)\n")
	} else {
		type toolStat struct {
			Name  string
			Count int
		}
		tools := make([]toolStat, 0, len(sa.ToolCounts))
		for name, count := range sa.ToolCounts {
			tools = append(tools, toolStat{Name: name, Count: count})
		}
		sort.Slice(tools, func(i, j int) bool {
			return tools[i].Count > tools[j].Count
		})
		for _, t := range tools {
			pct := 0
			if sa.TotalToolCalls > 0 {
				pct = (t.Count * 100) / sa.TotalToolCalls
			}
			sb.WriteString(fmt.Sprintf("   • %-22s: %3d calls (%2d%%)\n", t.Name, t.Count, pct))
		}
	}

	if len(sa.TopCommands) > 0 {
		sb.WriteString("\n💻 Top Executed Shell Commands:\n")
		for i, cmd := range sa.TopCommands {
			if i >= 5 {
				break
			}
			displayCmd := cmd.Command
			if len(displayCmd) > 60 {
				displayCmd = displayCmd[:57] + "..."
			}
			sb.WriteString(fmt.Sprintf("   [%d×] %s\n", cmd.Count, displayCmd))
		}
	}

	sb.WriteString("\n⚠️  Efficiency & Loop Detection:\n")
	if len(sa.Repetitions) == 0 {
		sb.WriteString("   ✅ No repetitive tool loops detected. Session ran cleanly.\n")
	} else {
		for _, rep := range sa.Repetitions {
			sb.WriteString(fmt.Sprintf("   ⚠️  %s\n", rep.Message))
		}
	}

	// If filter options provided, print filtered tool call log
	if len(opts) > 0 {
		opt := opts[0]
		filtered := sa.FilterToolCalls(opt)
		filterLabel := "Filtered Tool Calls"
		if opt.AutonomousOnly {
			filterLabel = "⚡ Autonomous Loop Tool Calls"
		} else if opt.ApprovedOnly {
			filterLabel = "✋ User Approved Tool Calls"
		} else if opt.PromptedOnly {
			filterLabel = "👤 Prompt-Triggered Tool Calls"
		} else if opt.FailedOnly {
			filterLabel = "❌ Failed Tool Calls"
		}
		if opt.ToolFilter != "" {
			filterLabel += fmt.Sprintf(" [%s]", opt.ToolFilter)
		}

		sb.WriteString(fmt.Sprintf("\n📋 %s (%d found):\n", filterLabel, len(filtered)))
		for _, c := range filtered {
			statusIcon := "✅"
			if c.IsFailed {
				statusIcon = "❌"
			}
			modeTag := "[auto]"
			if c.IsUserApproved {
				modeTag = "[apprv]"
			} else if c.IsPromptTriggered {
				modeTag = "[user]"
			}
			sb.WriteString(fmt.Sprintf("   %s %-6s Step #%-3d %-16s %s\n", statusIcon, modeTag, c.StepIndex, c.Tool, c.Summary))
			if c.IsFailed && c.FailureReason != "" {
				sb.WriteString(fmt.Sprintf("      ↳ ❌ Error: %s\n", c.FailureReason))
			}
		}
	}

	return sb.String()
}

// extractConversationID attempts to retrieve the conversation UUID from a transcript path.
func extractConversationID(transcriptPath string) string {
	parts := strings.Split(transcriptPath, "/brain/")
	if len(parts) > 1 {
		subParts := strings.Split(parts[1], "/")
		if len(subParts) > 0 && subParts[0] != "" {
			return subParts[0]
		}
	}
	return ""
}

// LoadToolConfirmations searches Antigravity CLI log files for interactive tool confirmations
// belonging to the given conversation ID. Returns a map of stepIdx -> approved.
func LoadToolConfirmations(convID string, customLogDir ...string) map[int]bool {
	confirmations := make(map[int]bool)
	if convID == "" {
		return confirmations
	}

	var searchDirs []string
	if len(customLogDir) > 0 && customLogDir[0] != "" {
		searchDirs = append(searchDirs, customLogDir[0])
	}

	home, err := os.UserHomeDir()
	if err == nil {
		searchDirs = append(searchDirs,
			filepath.Join(home, ".gemini", "antigravity-cli", "log"),
			filepath.Join(home, ".gemini", "antigravity", "log"),
		)
	}

	targetConvPattern := "convID=" + convID
	serverConvPattern := "Tool confirmation for conversation " + convID

	for _, dir := range searchDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}

		type fileInfo struct {
			path    string
			modTime time.Time
		}
		var files []fileInfo
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".log") {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			files = append(files, fileInfo{
				path:    filepath.Join(dir, entry.Name()),
				modTime: info.ModTime(),
			})
		}

		// Check newest logs first
		sort.Slice(files, func(i, j int) bool {
			return files[i].modTime.After(files[j].modTime)
		})

		convIDBytes := []byte(convID)
		for _, f := range files {
			data, err := os.ReadFile(f.path)
			if err != nil {
				continue
			}
			// Fast pre-filter: skip files that don't reference this conversation ID
			if !bytes.Contains(data, convIDBytes) {
				continue
			}

			scanner := bufio.NewScanner(bytes.NewReader(data))
			buf := make([]byte, 64*1024)
			scanner.Buffer(buf, 2*1024*1024)

			for scanner.Scan() {
				line := scanner.Text()

				// Format 1: input_loop.go: Responding to tool confirmation: convID=..., stepIdx=..., approved=true...
				if strings.Contains(line, "Responding to tool confirmation:") && strings.Contains(line, targetConvPattern) {
					stepIdx, approved, ok := parseInputLoopConfirmation(line)
					if ok {
						confirmations[stepIdx] = approved
					}
					continue
				}

				// Format 2: server.go: Tool confirmation for conversation <convID> step <stepIdx> (... approved=true)
				if strings.Contains(line, serverConvPattern) {
					stepIdx, approved, ok := parseServerConfirmation(line)
					if ok {
						confirmations[stepIdx] = approved
					}
					continue
				}
			}
		}
	}

	return confirmations
}

func parseInputLoopConfirmation(line string) (int, bool, bool) {
	idxStep := strings.Index(line, "stepIdx=")
	if idxStep == -1 {
		return 0, false, false
	}
	sub := line[idxStep+len("stepIdx="):]
	endIdx := strings.IndexAny(sub, ", \t\r\n")
	if endIdx != -1 {
		sub = sub[:endIdx]
	}
	stepIdx, err := strconv.Atoi(strings.TrimSpace(sub))
	if err != nil {
		return 0, false, false
	}

	idxApp := strings.Index(line, "approved=")
	if idxApp == -1 {
		return 0, false, false
	}
	subApp := line[idxApp+len("approved="):]
	endApp := strings.IndexAny(subApp, ", \t\r\n")
	if endApp != -1 {
		subApp = subApp[:endApp]
	}
	approved := strings.EqualFold(strings.TrimSpace(subApp), "true")

	return stepIdx, approved, true
}

func parseServerConfirmation(line string) (int, bool, bool) {
	idxStep := strings.Index(line, " step ")
	if idxStep == -1 {
		return 0, false, false
	}
	sub := line[idxStep+len(" step "):]
	endIdx := strings.IndexAny(sub, " (,\t\r\n")
	if endIdx != -1 {
		sub = sub[:endIdx]
	}
	stepIdx, err := strconv.Atoi(strings.TrimSpace(sub))
	if err != nil {
		return 0, false, false
	}

	idxApp := strings.Index(line, "approved=")
	if idxApp == -1 {
		return 0, false, false
	}
	subApp := line[idxApp+len("approved="):]
	endApp := strings.IndexAny(subApp, ") \t\r\n")
	if endApp != -1 {
		subApp = subApp[:endApp]
	}
	approved := strings.EqualFold(strings.TrimSpace(subApp), "true")

	return stepIdx, approved, true
}

// ApplyConfirmations retroactively updates tool call records with confirmation results.
func (sa *SessionAnalytics) ApplyConfirmations(confirmations map[int]bool) {
	if len(confirmations) == 0 {
		return
	}
	for i := range sa.ToolCalls {
		rec := &sa.ToolCalls[i]
		if rec.RequiresApproval {
			continue // already processed
		}
		approved, isConfirmed := confirmations[rec.StepIndex]
		if !isConfirmed {
			approved, isConfirmed = confirmations[rec.StepIndex+1]
		}
		if isConfirmed {
			rec.RequiresApproval = true
			rec.IsUserApproved = approved
			if approved {
				rec.UserConfirmation = "approved"
			} else {
				rec.UserConfirmation = "rejected"
			}
			if rec.IsAutonomous {
				rec.IsAutonomous = false
				if sa.AutonomousToolCalls > 0 {
					sa.AutonomousToolCalls--
				}
			} else if rec.IsPromptTriggered {
				if sa.PromptedToolCalls > 0 {
					sa.PromptedToolCalls--
				}
			}
			sa.ApprovedToolCalls++
		}
	}
}
