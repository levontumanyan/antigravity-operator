package analytics

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAnalyzeTranscript(t *testing.T) {
	tempDir := t.TempDir()
	logPath := filepath.Join(tempDir, "transcript.jsonl")

	sampleContent := `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-10-01T10:00:00Z","content":"Please check the status"}
{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-10-01T10:00:05Z","tool_calls":[{"name":"run_command","args":{"CommandLine":"git status"}}]}
{"step_index":2,"source":"MODEL","type":"GENERIC","status":"DONE","created_at":"2026-10-01T10:00:06Z","content":"The command exited with code 0."}
{"step_index":3,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-10-01T10:00:10Z","tool_calls":[{"name":"run_command","args":{"CommandLine":"git status"}}]}
{"step_index":4,"source":"MODEL","type":"GENERIC","status":"DONE","created_at":"2026-10-01T10:00:11Z","content":"The command exited with code 1."}
{"step_index":5,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-10-01T10:00:15Z","tool_calls":[{"name":"run_command","args":{"CommandLine":"git status"}}]}
{"step_index":6,"source":"MODEL","type":"GENERIC","status":"DONE","created_at":"2026-10-01T10:00:16Z","content":"The command exited with code 0."}
{"step_index":7,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-10-01T10:00:20Z","tool_calls":[{"name":"view_file","args":{"AbsolutePath":"/tmp/test.go"}}]}
{"step_index":8,"source":"MODEL","type":"GENERIC","status":"DONE","created_at":"2026-10-01T10:00:21Z","content":"File contents"}
{"step_index":9,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-10-01T10:00:25Z","tool_calls":[{"name":"manage_task","args":{"Action":"status","TaskId":"task-123"}}]}
{"step_index":10,"source":"MODEL","type":"GENERIC","status":"DONE","created_at":"2026-10-01T10:00:26Z","content":"Task running"}
{"step_index":11,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-10-01T10:00:30Z","tool_calls":[{"name":"manage_task","args":{"Action":"status","TaskId":"task-123"}}]}
{"step_index":12,"source":"MODEL","type":"GENERIC","status":"DONE","created_at":"2026-10-01T10:00:31Z","content":"Task done"}
`
	if err := os.WriteFile(logPath, []byte(sampleContent), 0644); err != nil {
		t.Fatalf("failed to write test transcript: %v", err)
	}

	// Add 3rd check to satisfy >= 3 polling loop threshold
	sampleContentWith3rdCheck := sampleContent + `{"step_index":13,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-10-01T10:00:35Z","tool_calls":[{"name":"manage_task","args":{"Action":"status","TaskId":"task-123"}}]}
{"step_index":14,"source":"MODEL","type":"GENERIC","status":"DONE","created_at":"2026-10-01T10:00:36Z","content":"Task finished"}
`
	if err := os.WriteFile(logPath, []byte(sampleContentWith3rdCheck), 0644); err != nil {
		t.Fatalf("failed to write test transcript: %v", err)
	}

	sa, err := AnalyzeTranscript(logPath)
	if err != nil {
		t.Fatalf("AnalyzeTranscript failed: %v", err)
	}

	if sa.TotalSteps != 15 {
		t.Errorf("expected 15 steps, got %d", sa.TotalSteps)
	}
	if sa.UserTurns != 1 {
		t.Errorf("expected 1 user turn, got %d", sa.UserTurns)
	}
	if sa.TotalToolCalls != 7 {
		t.Errorf("expected 7 tool calls, got %d", sa.TotalToolCalls)
	}
	if sa.FailedToolCalls != 1 {
		t.Errorf("expected 1 failed tool call, got %d", sa.FailedToolCalls)
	}

	// Verify repetition alerts
	hasCommandLoop := false
	hasPollingLoop := false
	for _, alert := range sa.Repetitions {
		if alert.Type == "command_loop" && alert.Target == "git status" {
			hasCommandLoop = true
		}
		if alert.Type == "polling_loop" && alert.Target == "task-123" {
			hasPollingLoop = true
		}
	}

	if !hasCommandLoop {
		t.Errorf("expected command_loop alert for git status, got %v", sa.Repetitions)
	}
	if !hasPollingLoop {
		t.Errorf("expected polling_loop alert for task-123, got %v", sa.Repetitions)
	}
}

func TestAnalyzeTranscript_Empty(t *testing.T) {
	tempDir := t.TempDir()
	logPath := filepath.Join(tempDir, "empty.jsonl")
	if err := os.WriteFile(logPath, []byte(""), 0644); err != nil {
		t.Fatalf("failed to write empty file: %v", err)
	}

	sa, err := AnalyzeTranscript(logPath)
	if err != nil {
		t.Fatalf("unexpected error on empty file: %v", err)
	}
	if sa.TotalSteps != 0 {
		t.Errorf("expected 0 steps, got %d", sa.TotalSteps)
	}
}

func TestAnalyzeTranscript_FIFOResetOnUserInput(t *testing.T) {
	tempDir := t.TempDir()
	logPath := filepath.Join(tempDir, "fifo_reset.jsonl")

	// Call 1 is unanswered / cancelled, then USER_INPUT comes in, then Call 2 fails.
	content := `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-10-01T10:00:00Z","content":"First prompt"}
{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-10-01T10:00:05Z","tool_calls":[{"name":"run_command","args":{"CommandLine":"long-task"}}]}
{"step_index":2,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-10-01T10:00:10Z","content":"Cancel and do something else"}
{"step_index":3,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-10-01T10:00:15Z","tool_calls":[{"name":"run_command","args":{"CommandLine":"make build"}}]}
{"step_index":4,"source":"MODEL","type":"GENERIC","status":"DONE","created_at":"2026-10-01T10:00:20Z","content":"The command exited with code 1."}
`
	if err := os.WriteFile(logPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	sa, err := AnalyzeTranscript(logPath)
	if err != nil {
		t.Fatalf("AnalyzeTranscript failed: %v", err)
	}

	if len(sa.ToolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(sa.ToolCalls))
	}
	// First call was cancelled (not marked failed by the second call's result)
	if sa.ToolCalls[0].IsFailed {
		t.Errorf("first tool call should not be marked failed")
	}
	// Second call should correctly receive the failure
	if !sa.ToolCalls[1].IsFailed {
		t.Errorf("second tool call should be marked failed")
	}
	if sa.ToolCalls[1].Command != "make build" {
		t.Errorf("expected second call command 'make build', got %q", sa.ToolCalls[1].Command)
	}
}

func TestAnalyzeTranscript_MaskedExitCode(t *testing.T) {
	tempDir := t.TempDir()
	logPath := filepath.Join(tempDir, "masked_exit.jsonl")

	// Output contains "exited with code 0" from an earlier echo, but the final exit code is 2.
	content := `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-10-01T10:00:00Z","content":"Run script"}
{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-10-01T10:00:05Z","tool_calls":[{"name":"run_command","args":{"CommandLine":"./script.sh"}}]}
{"step_index":2,"source":"MODEL","type":"GENERIC","status":"DONE","created_at":"2026-10-01T10:00:10Z","content":"Substep 1: The command exited with code 0\nSubstep 2: failed!\nThe command exited with code 2."}
`
	if err := os.WriteFile(logPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	sa, err := AnalyzeTranscript(logPath)
	if err != nil {
		t.Fatalf("AnalyzeTranscript failed: %v", err)
	}

	if sa.FailedToolCalls != 1 {
		t.Errorf("expected 1 failed tool call despite earlier 0 code, got %d", sa.FailedToolCalls)
	}
	if len(sa.ToolCalls) > 0 && !sa.ToolCalls[0].IsFailed {
		t.Errorf("expected tool call to be marked failed")
	}
}

func TestAnalyzeTranscript_ConsecutiveLoopCount(t *testing.T) {
	tempDir := t.TempDir()
	logPath := filepath.Join(tempDir, "consecutive.jsonl")

	var sb []string
	sb = append(sb, `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-10-01T10:00:00Z","content":"Loop test"}`)
	for i := 1; i <= 6; i++ {
		sb = append(sb, `{"step_index":`+string(rune('0'+i))+`,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-10-01T10:00:05Z","tool_calls":[{"name":"run_command","args":{"CommandLine":"pytest"}}]}`)
		sb = append(sb, `{"step_index":10,"source":"MODEL","type":"GENERIC","status":"DONE","created_at":"2026-10-01T10:00:06Z","content":"The command exited with code 0."}`)
	}

	if err := os.WriteFile(logPath, []byte(string(rune(0))), 0644); err != nil {
		t.Fatalf("failed to init file")
	}
	raw := ""
	for _, l := range sb {
		raw += l + "\n"
	}
	_ = os.WriteFile(logPath, []byte(raw), 0644)

	sa, err := AnalyzeTranscript(logPath)
	if err != nil {
		t.Fatalf("AnalyzeTranscript failed: %v", err)
	}

	var foundAlert *RepetitionAlert
	for i := range sa.Repetitions {
		if sa.Repetitions[i].Target == "pytest" {
			foundAlert = &sa.Repetitions[i]
			break
		}
	}
	if foundAlert == nil {
		t.Fatalf("expected loop alert for pytest")
	}
	if foundAlert.Count != 6 {
		t.Errorf("expected count 6 for consecutive loop, got %d", foundAlert.Count)
	}
}

func TestAnalyzeTranscript_InterveningToolBreaksConsecutive(t *testing.T) {
	tempDir := t.TempDir()
	logPath := filepath.Join(tempDir, "intervening.jsonl")

	content := `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-10-01T10:00:00Z","content":"Intervening test"}
{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-10-01T10:00:01Z","tool_calls":[{"name":"run_command","args":{"CommandLine":"git status"}}]}
{"step_index":2,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-10-01T10:00:02Z","tool_calls":[{"name":"run_command","args":{"CommandLine":"git status"}}]}
{"step_index":3,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-10-01T10:00:03Z","tool_calls":[{"name":"view_file","args":{"AbsolutePath":"/tmp/foo.txt"}}]}
{"step_index":4,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-10-01T10:00:04Z","tool_calls":[{"name":"run_command","args":{"CommandLine":"git status"}}]}
`
	if err := os.WriteFile(logPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	sa, err := AnalyzeTranscript(logPath)
	if err != nil {
		t.Fatalf("AnalyzeTranscript failed: %v", err)
	}

	// git status ran 3 times across session, but never 3 times in a row!
	for _, alert := range sa.Repetitions {
		if alert.Target == "git status" {
			if alert.Message != `Repetitive command: "git status" executed 3 times across session` {
				t.Errorf("expected session-wide alert, not consecutive in a row: %s", alert.Message)
			}
		}
	}
}

func TestExtractCleanPrompt_UTF8(t *testing.T) {
	longNonASCII := "Olá mundo! Você pode me ajudar com isso? 🚀✨ Teste de acentuação em português e caracteres especiais."
	result := extractCleanPrompt(longNonASCII)
	if len([]rune(result)) > 60 {
		t.Errorf("expected at most 60 runes, got %d", len([]rune(result)))
	}
	// Verify valid UTF-8
	for _, r := range result {
		if r == '\ufffd' {
			t.Errorf("found invalid UTF-8 replacement char in result: %q", result)
		}
	}
}
