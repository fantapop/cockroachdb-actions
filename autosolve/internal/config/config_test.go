package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestLoadAssessConfig_RequiresPromptOrSkill(t *testing.T) {
	clearInputEnv(t)
	_, err := LoadAssessConfig()
	if err == nil {
		t.Fatal("expected error when neither prompt nor skill is set")
	}
}

func TestLoadAssessConfig_AcceptsPrompt(t *testing.T) {
	clearInputEnv(t)
	t.Setenv("INPUT_SYSTEM_PROMPT", "fix the bug")
	t.Setenv("INPUT_MODEL", "claude-opus-4-6")
	cfg, err := LoadAssessConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SystemPrompt != "fix the bug" {
		t.Errorf("expected prompt 'fix the bug', got %q", cfg.SystemPrompt)
	}
	if cfg.FooterType != "assessment" {
		t.Errorf("expected footer type 'assessment', got %q", cfg.FooterType)
	}
}

func TestLoadAssessConfig_AcceptsSkill(t *testing.T) {
	clearInputEnv(t)
	t.Setenv("INPUT_SKILL", "path/to/skill.md")
	t.Setenv("INPUT_MODEL", "claude-opus-4-6")
	cfg, err := LoadAssessConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Skill != "path/to/skill.md" {
		t.Errorf("expected skill path, got %q", cfg.Skill)
	}
}

func TestLoadImplementConfig_ValidatesPR(t *testing.T) {
	clearInputEnv(t)
	t.Setenv("INPUT_SYSTEM_PROMPT", "fix it")
	t.Setenv("INPUT_MODEL", "claude-opus-4-6")
	// Missing fork_owner, fork_repo, etc.
	_, err := LoadImplementConfig()
	if err == nil {
		t.Fatal("expected error when PR inputs are missing")
	}
}

func TestLoadImplementConfig_Defaults(t *testing.T) {
	clearInputEnv(t)
	t.Setenv("INPUT_SYSTEM_PROMPT", "fix it")
	t.Setenv("INPUT_MODEL", "claude-opus-4-6")
	t.Setenv("INPUT_FORK_OWNER", "testorg")
	t.Setenv("INPUT_FORK_REPO", "testrepo")
	t.Setenv("INPUT_FORK_PUSH_TOKEN", "fake-token")
	t.Setenv("INPUT_PR_CREATE_TOKEN", "fake-token")
	t.Setenv("SCRIPTS_DIR", "/tmp/scripts")
	cfg, err := LoadImplementConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxRetries != 3 {
		t.Errorf("expected MaxRetries=3, got %d", cfg.MaxRetries)
	}
	if cfg.GitUserName != "autosolve[bot]" {
		t.Errorf("expected default git user name, got %q", cfg.GitUserName)
	}
}

func TestValidateAuth_Vertex(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv("CLAUDE_CODE_USE_VERTEX", "1")
	t.Setenv("ANTHROPIC_VERTEX_PROJECT_ID", "my-project")
	t.Setenv("CLOUD_ML_REGION", "us-central1")
	if err := ValidateAuth(); err != nil {
		t.Errorf("expected no error with Vertex, got: %v", err)
	}
}

func TestValidateAuth_VertexMissing(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv("CLAUDE_CODE_USE_VERTEX", "1")
	err := ValidateAuth()
	if err == nil {
		t.Fatal("expected error when Vertex config is incomplete")
	}
}

func TestValidateAuth_NotEnabled(t *testing.T) {
	clearAuthEnv(t)
	err := ValidateAuth()
	if err == nil {
		t.Fatal("expected error when Vertex is not enabled")
	}
}

func TestLoadSandboxConfig_RejectsWorkspaceRoot(t *testing.T) {
	clearInputEnv(t)
	// setupSandboxEnv (called via clearInputEnv) puts cwd at workspace/repo.
	// Move cwd up to GITHUB_WORKSPACE itself; load should reject this.
	if err := os.Chdir(os.Getenv("GITHUB_WORKSPACE")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("INPUT_SYSTEM_PROMPT", "fix")
	t.Setenv("INPUT_MODEL", "claude-opus-4-6")

	_, err := LoadAssessConfig()
	if err == nil {
		t.Fatal("expected error when cwd is GITHUB_WORKSPACE")
	}
	if !strings.Contains(err.Error(), "subdirectory") {
		t.Errorf("expected error mentioning subdirectory; got: %v", err)
	}
}

func TestLoadSandboxConfig_RejectsCwdOutsideWorkspace(t *testing.T) {
	clearInputEnv(t)
	// Move cwd outside GITHUB_WORKSPACE.
	outside := t.TempDir()
	if err := os.Chdir(outside); err != nil {
		t.Fatal(err)
	}
	t.Setenv("INPUT_SYSTEM_PROMPT", "fix")
	t.Setenv("INPUT_MODEL", "claude-opus-4-6")

	_, err := LoadAssessConfig()
	if err == nil {
		t.Fatal("expected error when cwd is outside GITHUB_WORKSPACE")
	}
}

func TestLoadSandboxConfig_PopulatesPaths(t *testing.T) {
	clearInputEnv(t)
	t.Setenv("INPUT_SYSTEM_PROMPT", "fix")
	t.Setenv("INPUT_MODEL", "claude-opus-4-6")

	// Both an absolute path and a workspace-relative path should resolve.
	abs := t.TempDir()
	t.Setenv("INPUT_READ_PATHS", abs+", repo")

	cfg, err := LoadAssessConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WorkingDir == "" {
		t.Error("expected WorkingDir to be populated")
	}
	if cfg.ScratchDir == "" {
		t.Error("expected ScratchDir to be populated")
	}
	if !strings.HasSuffix(cfg.ScratchDir, "autosolve-scratch") {
		t.Errorf("expected scratch dir to be under RUNNER_TEMP/autosolve-scratch; got %q", cfg.ScratchDir)
	}
	if len(cfg.ReadPaths) != 2 {
		t.Fatalf("expected 2 additional read paths; got %v", cfg.ReadPaths)
	}
	for _, p := range cfg.ReadPaths {
		if !filepath.IsAbs(p) {
			t.Errorf("expected absolute additional read path; got %q", p)
		}
	}
}

func TestLoadSandboxConfig_AutoBindsApplicationCredentials(t *testing.T) {
	clearInputEnv(t)
	t.Setenv("INPUT_SYSTEM_PROMPT", "fix")
	t.Setenv("INPUT_MODEL", "claude-opus-4-6")

	credsDir := t.TempDir()
	credsPath := filepath.Join(credsDir, "gha-creds-abc.json")
	if err := os.WriteFile(credsPath, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", credsPath)

	cfg, err := LoadAssessConfig()
	if err != nil {
		t.Fatal(err)
	}

	resolvedCreds, err := filepath.EvalSymlinks(credsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cfg.ReadPaths, resolvedCreds) {
		t.Errorf("expected GOOGLE_APPLICATION_CREDENTIALS %q to be auto-bound; got %v",
			resolvedCreds, cfg.ReadPaths)
	}
}

func TestParseBlockedPaths(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"empty defaults", "", []string{".github/"}},
		{"caller adds extra", "secrets/, .env", []string{".github/", ".env", "secrets/"}},
		{"caller includes required", ".github/, secrets/", []string{".github/", "secrets/"}},
		{"caller cannot remove required", "only-this/", []string{".github/", "only-this/"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseBlockedPaths(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("len mismatch: got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("index %d: got %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestEnvBool(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		def     bool
		want    bool
		wantErr bool
	}{
		{"empty uses default true", "", true, true, false},
		{"empty uses default false", "", false, false, false},
		{"lowercase true", "true", false, true, false},
		{"uppercase TRUE", "TRUE", false, true, false},
		{"mixed case True", "True", false, true, false},
		{"lowercase false", "false", true, false, false},
		{"uppercase FALSE", "FALSE", true, false, false},
		{"invalid value", "yes", false, false, true},
		{"numeric value", "1", false, false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := "TEST_ENV_BOOL"
			if tt.value == "" {
				os.Unsetenv(key)
			} else {
				t.Setenv(key, tt.value)
			}
			got, err := envBool(key, tt.def)
			if (err != nil) != tt.wantErr {
				t.Fatalf("envBool() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("envBool() = %v, want %v", got, tt.want)
			}
		})
	}
}

func clearInputEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"INPUT_SYSTEM_PROMPT", "INPUT_SKILL", "INPUT_MODEL",
		"INPUT_ASSESSMENT_CRITERIA", "INPUT_LOG_LEVEL",
		"INPUT_BLOCKED_PATHS", "INPUT_MAX_RETRIES", "INPUT_ALLOWED_TOOLS",
		"INPUT_FORK_OWNER", "INPUT_FORK_REPO",
		"INPUT_FORK_PUSH_TOKEN", "INPUT_PR_CREATE_TOKEN",
		"INPUT_READ_PATHS",
	} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
	setupSandboxEnv(t)
}

// setupSandboxEnv arranges a valid sandbox environment for config loading:
// a temp GITHUB_WORKSPACE, a sibling RUNNER_TEMP, and a subdirectory of
// the workspace as cwd (so the workspace-root rejection rule passes).
func setupSandboxEnv(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	runnerTemp := filepath.Join(root, "runner-temp")
	repo := filepath.Join(workspace, "repo")
	for _, p := range []string{workspace, runnerTemp, repo} {
		if err := os.MkdirAll(p, 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GITHUB_WORKSPACE", workspace)
	t.Setenv("RUNNER_TEMP", runnerTemp)
	// Clear any inherited creds env var so the auto-bind path doesn't add an
	// extra read path that the test isn't expecting.
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	os.Unsetenv("GOOGLE_APPLICATION_CREDENTIALS")

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

func clearAuthEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"CLAUDE_CODE_USE_VERTEX",
		"ANTHROPIC_VERTEX_PROJECT_ID", "CLOUD_ML_REGION",
	} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
}

func TestParseCSV(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"empty", "", nil},
		{"single", "ISSUE_TITLE", []string{"ISSUE_TITLE"}},
		{"multiple", "ISSUE_TITLE,ISSUE_BODY", []string{"ISSUE_TITLE", "ISSUE_BODY"}},
		{"with whitespace", " ISSUE_TITLE , ISSUE_BODY ", []string{"ISSUE_TITLE", "ISSUE_BODY"}},
		{"trailing comma", "FOO,", []string{"FOO"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseContextVars(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("parseContextVars(%q) = %v, want %v", tt.input, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("index %d: got %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestParseLogLevel(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"empty defaults to error", "", "error", false},
		{"error", "error", "error", false},
		{"info", "info", "info", false},
		{"debug", "debug", "debug", false},
		{"uppercase", "INFO", "info", false},
		{"mixed case", "Debug", "debug", false},
		{"with whitespace", " info ", "info", false},
		{"invalid value", "verbose", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseLogLevel(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseLogLevel(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("parseLogLevel(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestLoadAssessConfig_ContextVars(t *testing.T) {
	clearInputEnv(t)
	t.Setenv("INPUT_SYSTEM_PROMPT", "fix it")
	t.Setenv("INPUT_MODEL", "claude-opus-4-6")
	t.Setenv("INPUT_CONTEXT_VARS", "ISSUE_TITLE,ISSUE_BODY")
	cfg, err := LoadAssessConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.ContextVars) != 2 {
		t.Fatalf("expected 2 context vars, got %d", len(cfg.ContextVars))
	}
	if cfg.ContextVars[0] != "ISSUE_TITLE" || cfg.ContextVars[1] != "ISSUE_BODY" {
		t.Errorf("unexpected context vars: %v", cfg.ContextVars)
	}
}
