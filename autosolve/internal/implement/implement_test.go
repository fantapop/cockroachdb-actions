package implement

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/actions/autosolve/internal/claude"
	"github.com/cockroachdb/actions/autosolve/internal/config"
	"github.com/cockroachdb/actions/autosolve/internal/github"
)

type mockRunner struct {
	calls      int
	results    []string // result text per attempt
	sessionIDs []string
	exitCodes  []int
}

func (m *mockRunner) Run(ctx context.Context, opts claude.RunOptions) (*claude.Result, error) {
	idx := m.calls
	m.calls++

	resultText := ""
	if idx < len(m.results) {
		resultText = m.results[idx]
	}
	sessionID := ""
	if idx < len(m.sessionIDs) {
		sessionID = m.sessionIDs[idx]
	}
	exitCode := 0
	if idx < len(m.exitCodes) {
		exitCode = m.exitCodes[idx]
	}

	// Write mock output to the output file
	out := struct {
		Type      string `json:"type"`
		Result    string `json:"result"`
		SessionID string `json:"session_id"`
	}{
		Type:      "result",
		Result:    resultText,
		SessionID: sessionID,
	}
	data, _ := json.Marshal(out)
	os.WriteFile(opts.OutputFile, data, 0644)

	// Simulate Claude writing metadata files on implementation success.
	// Implement.Run sets these env vars to the scratch paths before
	// invoking the runner, mirroring how the real claude subprocess
	// would discover them via printenv.
	if strings.Contains(resultText, "IMPLEMENTATION_RESULT - SUCCESS") {
		if p := os.Getenv("AUTOSOLVE_COMMIT_MESSAGE_PATH"); p != "" {
			os.WriteFile(p, []byte("fix: mock commit"), 0644)
		}
		if p := os.Getenv("AUTOSOLVE_PR_BODY_PATH"); p != "" {
			os.WriteFile(p, []byte("Mock PR body."), 0644)
		}
	}

	result := &claude.Result{
		ResultText: resultText,
		SessionID:  sessionID,
		ExitCode:   exitCode,
	}
	if resultText == "" {
		return result, fmt.Errorf("%w (exit code %d)", claude.ErrEmptyResult, exitCode)
	}
	return result, nil
}

type mockGHClient struct {
	labels []string
	prURL  string
	prErr  error
}

func (m *mockGHClient) CreatePR(_ context.Context, opts github.PullRequestOptions) (string, error) {
	if m.prErr != nil {
		return "", m.prErr
	}
	return m.prURL, nil
}

func (m *mockGHClient) CreateLabel(_ context.Context, _ string, name string) error {
	m.labels = append(m.labels, name)
	return nil
}

func (m *mockGHClient) BranchExists(_ context.Context, _, _ string) (bool, error) {
	return false, nil
}

type mockGitClient struct {
	hasStagedChanges bool
	resetHeadCalls   int
}

func (m *mockGitClient) Diff(args ...string) (string, error) {
	if !m.hasStagedChanges {
		return "", nil
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--cached") && strings.Contains(joined, "--name-only") {
		return "src/main.go\n", nil
	}
	if strings.Contains(joined, "--cached") {
		return "diff --git a/src/main.go\n", nil
	}
	return "", nil
}
func (m *mockGitClient) LsFiles(args ...string) (string, error) { return "", nil }
func (m *mockGitClient) Config(args ...string) error            { return nil }
func (m *mockGitClient) Remote(args ...string) (string, error)  { return "", nil }
func (m *mockGitClient) Checkout(args ...string) error          { return nil }
func (m *mockGitClient) Add(args ...string) error               { return nil }
func (m *mockGitClient) Commit(message string) error            { return nil }
func (m *mockGitClient) Push(args ...string) error              { return nil }
func (m *mockGitClient) ResetHead() error                       { m.resetHeadCalls++; return nil }

func init() {
	RetryDelay = 0 * time.Millisecond
}

func TestRun_Success(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("GITHUB_OUTPUT", tmpDir+"/output")
	t.Setenv("GITHUB_STEP_SUMMARY", tmpDir+"/summary")

	cfg := &config.Config{
		SystemPrompt: "Fix the bug",
		Model:        "sonnet",
		FooterType:   "implementation",
		MaxRetries:   3,
		AllowedTools: "Read,Write,Edit",
		ForkOwner:    "testorg",
		ForkRepo:     "testrepo",
		BranchPrefix: "autosolve/",
		PRBaseBranch: "main",
		ScratchDir:   tmpDir,
	}

	runner := &mockRunner{
		results: []string{
			"Fixed it.\n\nIMPLEMENTATION_RESULT - SUCCESS",
			"No issues found.\n\nSECURITY_REVIEW - SUCCESS",
		},
	}

	err := Run(context.Background(), cfg, runner, &mockGHClient{prURL: "https://github.com/org/repo/pull/1"}, &mockGitClient{hasStagedChanges: true}, tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	// 1 implement call + 1 security review call
	if runner.calls != 2 {
		t.Errorf("expected 2 calls, got %d", runner.calls)
	}
}

func TestRun_RetryThenSuccess(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("GITHUB_OUTPUT", tmpDir+"/output")
	t.Setenv("GITHUB_STEP_SUMMARY", tmpDir+"/summary")

	cfg := &config.Config{
		SystemPrompt: "Fix the bug",
		Model:        "sonnet",
		FooterType:   "implementation",
		MaxRetries:   3,
		AllowedTools: "Read,Write,Edit",
		ForkOwner:    "testorg",
		ForkRepo:     "testrepo",
		BranchPrefix: "autosolve/",
		PRBaseBranch: "main",
		ScratchDir:   tmpDir,
	}

	runner := &mockRunner{
		results: []string{
			"IMPLEMENTATION_RESULT - FAILED",
			"IMPLEMENTATION_RESULT - SUCCESS",
			"No issues found.\n\nSECURITY_REVIEW - SUCCESS",
		},
		sessionIDs: []string{"sess-1", "sess-1"},
	}

	err := Run(context.Background(), cfg, runner, &mockGHClient{prURL: "https://github.com/org/repo/pull/1"}, &mockGitClient{hasStagedChanges: true}, tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	// 2 implement calls + 1 security review call
	if runner.calls != 3 {
		t.Errorf("expected 3 calls (1 retry + security review), got %d", runner.calls)
	}
}

func TestRun_AllRetriesFail(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("GITHUB_OUTPUT", tmpDir+"/output")
	t.Setenv("GITHUB_STEP_SUMMARY", tmpDir+"/summary")

	cfg := &config.Config{
		SystemPrompt: "Fix the bug",
		Model:        "sonnet",
		FooterType:   "implementation",
		MaxRetries:   2,
		AllowedTools: "Read,Write,Edit",
		ForkOwner:    "testorg",
		ForkRepo:     "testrepo",
		BranchPrefix: "autosolve/",
		PRBaseBranch: "main",
		ScratchDir:   tmpDir,
	}

	runner := &mockRunner{
		results: []string{"IMPLEMENTATION_RESULT - FAILED", "IMPLEMENTATION_RESULT - FAILED"},
	}

	// Should return an error so the step exits non-zero.
	err := Run(context.Background(), cfg, runner, &mockGHClient{}, &mockGitClient{}, tmpDir)
	if err == nil {
		t.Fatal("expected error when all retries fail")
	}
	if runner.calls != 2 {
		t.Errorf("expected 2 calls, got %d", runner.calls)
	}
}

func TestExtractSummary(t *testing.T) {
	text := "Fixed the timeout issue.\nAdded test.\nIMPLEMENTATION_RESULT - SUCCESS"
	summary := extractSummary(text, "IMPLEMENTATION_RESULT")
	if summary != "Fixed the timeout issue.\nAdded test." {
		t.Errorf("unexpected summary: %q", summary)
	}
}

func TestWriteOutputs(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("GITHUB_OUTPUT", tmpDir+"/output")
	t.Setenv("GITHUB_STEP_SUMMARY", tmpDir+"/summary")

	err := writeOutputs("SUCCESS", "https://github.com/org/repo/pull/1", "autosolve/fix-123", "Done\nIMPLEMENTATION_RESULT - SUCCESS", nil)
	if err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(tmpDir + "/output")
	content := string(data)
	if content == "" {
		t.Error("expected outputs to be written")
	}

	summaryData, _ := os.ReadFile(tmpDir + "/summary")
	summary := string(summaryData)
	if summary == "" {
		t.Error("expected step summary to be written")
	}
}

func TestReadCommitMessage(t *testing.T) {
	scratch := t.TempDir()
	cfg := &config.Config{ScratchDir: scratch}
	path := commitMessagePath(cfg)

	t.Run("missing file returns error", func(t *testing.T) {
		_, _, err := readCommitMessage(cfg)
		if err == nil {
			t.Error("expected error when file is missing")
		}
	})

	t.Run("subject only", func(t *testing.T) {
		os.WriteFile(path, []byte("fix: broken build"), 0644)
		subject, body, err := readCommitMessage(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if subject != "fix: broken build" {
			t.Errorf("unexpected subject: %q", subject)
		}
		if body != "" {
			t.Errorf("expected empty body, got: %q", body)
		}
	})

	t.Run("subject and body", func(t *testing.T) {
		os.WriteFile(path, []byte("fix: broken build\n\nDetailed explanation here."), 0644)
		subject, body, err := readCommitMessage(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if subject != "fix: broken build" {
			t.Errorf("unexpected subject: %q", subject)
		}
		if body != "Detailed explanation here." {
			t.Errorf("unexpected body: %q", body)
		}
	})

	t.Run("file is removed after read", func(t *testing.T) {
		os.WriteFile(path, []byte("subject"), 0644)
		_, _, _ = readCommitMessage(cfg)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Error("expected file to be removed after read")
		}
	})
}

func TestBuildPRBody(t *testing.T) {
	t.Run("reads pr-body file", func(t *testing.T) {
		tmpDir := t.TempDir()
		os.WriteFile(filepath.Join(tmpDir, "autosolve-pr-body"), []byte("Custom PR body from Claude."), 0644)

		cfg := &config.Config{PRFooter: "-- footer"}
		body := buildPRBody(cfg, tmpDir)
		if body != "Custom PR body from Claude.\n\n-- footer" {
			t.Errorf("unexpected body: %q", body)
		}
	})

	t.Run("no template or file appends footer only", func(t *testing.T) {
		cfg := &config.Config{PRFooter: "-- footer"}
		body := buildPRBody(cfg, t.TempDir())
		if body != "\n\n-- footer" {
			t.Errorf("unexpected body: %q", body)
		}
	})
}

func TestAISecurityReview_NothingStaged(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{Model: "sonnet"}
	runner := &mockRunner{}
	gitClient := &mockGitClient{hasStagedChanges: false}
	var tracker claude.UsageTracker

	err := aiSecurityReview(context.Background(), cfg, runner, gitClient, tmpDir, &tracker)
	if err != nil {
		t.Fatal(err)
	}
	if runner.calls != 0 {
		t.Errorf("expected no runner calls when nothing staged, got %d", runner.calls)
	}
}

func TestAISecurityReview_Pass(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{Model: "sonnet"}
	runner := &mockRunner{
		results: []string{"No issues found.\n\nSECURITY_REVIEW - SUCCESS"},
	}
	gitClient := &mockGitClient{hasStagedChanges: true}
	var tracker claude.UsageTracker

	err := aiSecurityReview(context.Background(), cfg, runner, gitClient, tmpDir, &tracker)
	if err != nil {
		t.Fatal(err)
	}
	if runner.calls != 1 {
		t.Errorf("expected 1 runner call, got %d", runner.calls)
	}
	if gitClient.resetHeadCalls != 0 {
		t.Errorf("expected no reset on success, got %d", gitClient.resetHeadCalls)
	}
}

func TestAISecurityReview_Fail(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{Model: "sonnet"}
	runner := &mockRunner{
		results: []string{"Found hardcoded API key.\n\nSECURITY_REVIEW - FAILED"},
	}
	gitClient := &mockGitClient{hasStagedChanges: true}
	var tracker claude.UsageTracker

	err := aiSecurityReview(context.Background(), cfg, runner, gitClient, tmpDir, &tracker)
	if err == nil {
		t.Fatal("expected error when security review fails")
	}
	if !strings.Contains(err.Error(), "sensitive content") {
		t.Errorf("expected 'sensitive content' error, got: %v", err)
	}
	if gitClient.resetHeadCalls != 1 {
		t.Errorf("expected 1 reset call on failure, got %d", gitClient.resetHeadCalls)
	}
}
