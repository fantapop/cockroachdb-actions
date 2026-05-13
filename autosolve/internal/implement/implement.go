// Package implement orchestrates the implementation phase of autosolve,
// including retry logic, security checks, and PR creation.
package implement

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cockroachdb/actions/autosolve/internal/action"
	"github.com/cockroachdb/actions/autosolve/internal/claude"
	"github.com/cockroachdb/actions/autosolve/internal/config"
	"github.com/cockroachdb/actions/autosolve/internal/git"
	"github.com/cockroachdb/actions/autosolve/internal/github"
	"github.com/cockroachdb/actions/autosolve/internal/prompt"
	"github.com/cockroachdb/actions/autosolve/internal/security"
)

const retryPrompt = "The previous attempt did not succeed. Please review what went wrong, try a different approach if needed, and attempt the fix again. Remember to end your response with IMPLEMENTATION_RESULT - SUCCESS or IMPLEMENTATION_RESULT - FAILED."

// RetryDelay is the pause between retry attempts. Exported for testing.
var RetryDelay = 10 * time.Second

// Run executes the implementation phase.
func Run(
	ctx context.Context,
	cfg *config.Config,
	runner claude.Runner,
	ghClient github.Client,
	gitClient git.Client,
	tmpDir string,
) error {
	// Warn if the repo is missing recommended .gitignore patterns
	security.CheckGitignore(action.LogWarning)

	// Tell claude (via env vars + printenv permissions added below) where
	// to write the commit message and PR body. Both paths live in the
	// scratch dir, outside the repo, so they can never be accidentally
	// staged into a commit.
	if err := os.Setenv("AUTOSOLVE_COMMIT_MESSAGE_PATH", commitMessagePath(cfg)); err != nil {
		return fmt.Errorf("setting AUTOSOLVE_COMMIT_MESSAGE_PATH: %w", err)
	}
	if err := os.Setenv("AUTOSOLVE_PR_BODY_PATH", prBodyPath(cfg)); err != nil {
		return fmt.Errorf("setting AUTOSOLVE_PR_BODY_PATH: %w", err)
	}

	// Pre-compute branch name so we can check for conflicts before
	// spending tokens on Claude.
	suffix := cfg.BranchSuffix
	if suffix == "" {
		suffix = time.Now().Format("20060102-150405")
	}
	branchName := cfg.BranchPrefix + suffix

	forkRepo := fmt.Sprintf("%s/%s", cfg.ForkOwner, cfg.ForkRepo)
	exists, err := ghClient.BranchExists(ctx, forkRepo, branchName)
	if err != nil {
		return fmt.Errorf("checking branch availability: %w", err)
	}
	if exists {
		return fmt.Errorf("branch %q already exists on %s — delete it or use a different branch_suffix", branchName, forkRepo)
	}

	// Build prompt
	promptFile, err := prompt.Build(cfg, tmpDir)
	if err != nil {
		return fmt.Errorf("building prompt: %w", err)
	}

	action.LogInfo(fmt.Sprintf("Running implementation with model: %s (max retries: %d)", cfg.Model, cfg.MaxRetries))

	outputFile := filepath.Join(tmpDir, "implement-claude-output.json")

	var (
		result     *claude.Result
		implStatus = "FAILED"
		resultText string
		tracker    claude.UsageTracker
	)

	// Retry loop
	for attempt := 1; attempt <= cfg.MaxRetries; attempt++ {
		action.LogInfo(fmt.Sprintf("--- Attempt %d of %d ---", attempt, cfg.MaxRetries))

		// Append printenv permissions for each context var so Claude can
		// read them without unrestricted Bash access. Always permit
		// reading the autosolve coordination paths so Claude can find
		// the commit-message and pr-body destinations.
		extraTools := []string{
			"Bash(printenv AUTOSOLVE_COMMIT_MESSAGE_PATH)",
			"Bash(printenv AUTOSOLVE_PR_BODY_PATH)",
		}
		for _, v := range cfg.ContextVars {
			extraTools = append(extraTools, fmt.Sprintf("Bash(printenv %s)", v))
		}
		allowedTools := cfg.AllowedTools + "," + strings.Join(extraTools, ",")

		opts := claude.RunOptions{
			Model:        cfg.Model,
			AllowedTools: allowedTools,
			MaxTurns:     200,
			OutputFile:   outputFile,
			ContextVars:  cfg.ContextVars,
			LogLevel:     cfg.LogLevel,
			Sandbox: &claude.SandboxOptions{
				WorkingDir: cfg.WorkingDir,
				ScratchDir: cfg.ScratchDir,
				ReadPaths:  cfg.ReadPaths,
			},
		}

		if attempt == 1 {
			opts.PromptFile = promptFile
		} else {
			if result.SessionID == "" {
				action.LogWarning("No session ID from previous attempt; restarting with original prompt")
				opts.PromptFile = promptFile
			} else {
				opts.Resume = result.SessionID
				opts.RetryPrompt = retryPrompt
			}
		}

		description := fmt.Sprintf("implement (attempt %d)", attempt)
		if cfg.LogLevel != "error" {
			action.BeginLogGroup("Claude output: " + description)
		}
		var err error
		result, err = runner.Run(ctx, opts)
		if cfg.LogLevel != "error" {
			action.EndLogGroup()
		}
		claude.LogResult(&tracker, result, description)
		if err != nil {
			action.LogWarning(fmt.Sprintf("Claude failed on attempt %d: %v", attempt, err))
			continue
		}

		// Extract result
		var positive bool
		resultText, positive, err = claude.ExtractResult(outputFile, "IMPLEMENTATION_RESULT")
		if err != nil {
			action.LogWarning(fmt.Sprintf("No result text extracted from Claude output on attempt %d: %v — see uploaded artifacts for raw output - retrying", attempt, err))
			continue
		}
		action.LogInfo(fmt.Sprintf("Claude result (attempt %d):", attempt))
		action.LogInfo(resultText)

		if positive {
			// Claude must write the commit message and PR body to the
			// scratch dir. Treat a missing file as an incomplete attempt so
			// we retry rather than falling back to a low-quality message.
			if _, statErr := os.Stat(commitMessagePath(cfg)); statErr != nil {
				action.LogWarning(fmt.Sprintf("Attempt %d succeeded but commit-message was not written - retrying", attempt))
				continue
			}
			if _, statErr := os.Stat(prBodyPath(cfg)); statErr != nil {
				action.LogWarning(fmt.Sprintf("Attempt %d succeeded but pr-body was not written - retrying", attempt))
				continue
			}
			action.LogNotice(fmt.Sprintf("Implementation succeeded on attempt %d", attempt))
			implStatus = "SUCCESS"
			break
		}

		action.LogWarning(fmt.Sprintf("Attempt %d did not succeed", attempt))

		if attempt < cfg.MaxRetries {
			action.LogInfo(fmt.Sprintf("Waiting %s before retry...", RetryDelay))
			timer := time.NewTimer(RetryDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}

	// Security check
	if implStatus == "SUCCESS" {
		violations, err := security.Check(gitClient, cfg.BlockedPaths)
		if err != nil {
			return fmt.Errorf("security check: %w", err)
		}
		if len(violations) > 0 {
			for _, v := range violations {
				action.LogWarning(v)
			}
			_ = writeOutputs("FAILED", "", "", resultText, &tracker)
			return fmt.Errorf("security check failed: violations found in staged changes")
		}
		action.LogNotice("Security check passed")
	}

	// Stage, validate, and submit
	var prURL string
	if implStatus == "SUCCESS" {
		if err := setupForkRemote(cfg, gitClient, branchName); err != nil {
			// Write outputs before returning the error so status/summary are
			// available to subsequent workflow steps.
			_ = writeOutputs("FAILED", "", "", resultText, &tracker)
			return fmt.Errorf("PR creation failed: %w", err)
		}
		commitSubject, commitBody, err := stageChanges(cfg, gitClient, tmpDir)
		if err != nil {
			_ = writeOutputs("FAILED", "", "", resultText, &tracker)
			return fmt.Errorf("PR creation failed: %w", err)
		}
		if err := validateChanges(ctx, cfg, runner, gitClient, tmpDir, &tracker); err != nil {
			_ = writeOutputs("FAILED", "", "", resultText, &tracker)
			return fmt.Errorf("PR creation failed: %w", err)
		}
		title, err := commitAndPush(cfg, gitClient, branchName, commitSubject, commitBody)
		if err != nil {
			_ = writeOutputs("FAILED", "", "", resultText, &tracker)
			return fmt.Errorf("PR creation failed: %w", err)
		}
		var prErr error
		prURL, prErr = createPR(ctx, cfg, ghClient, tmpDir, branchName, title)
		if prErr != nil {
			_ = writeOutputs("FAILED", "", "", resultText, &tracker)
			return fmt.Errorf("PR creation failed: %w", prErr)
		}
	}

	status := "FAILED"
	if implStatus == "SUCCESS" && prURL != "" {
		status = "SUCCESS"
	}

	if err := writeOutputs(status, prURL, branchName, resultText, &tracker); err != nil {
		return err
	}
	if status != "SUCCESS" {
		return fmt.Errorf("implementation failed")
	}
	return nil
}

func setupForkRemote(cfg *config.Config, gitClient git.Client, branchName string) error {
	if err := gitClient.Config("user.name", cfg.GitUserName); err != nil {
		return fmt.Errorf("setting git user.name: %w", err)
	}
	if err := gitClient.Config("user.email", cfg.GitUserEmail); err != nil {
		return fmt.Errorf("setting git user.email: %w", err)
	}

	// Set fork credentials and GIT_ASKPASS for the git push subprocess
	// only, so the token is never in the broader process environment or
	// written to disk.
	if cliClient, ok := gitClient.(*git.CLIClient); ok {
		askpass := filepath.Join(os.Getenv("SCRIPTS_DIR"), "git-askpass.sh")
		cliClient.PushEnv = []string{
			"GIT_ASKPASS=" + askpass,
			"GIT_FORK_USER=" + cfg.ForkOwner,
			"GIT_FORK_PASSWORD=" + cfg.ForkPushToken,
			"GIT_TERMINAL_PROMPT=0",
		}
	}

	forkURL := fmt.Sprintf("https://github.com/%s/%s.git", cfg.ForkOwner, cfg.ForkRepo)

	remotes, err := gitClient.Remote()
	if err != nil {
		return fmt.Errorf("listing git remotes: %w", err)
	}
	hasFork := false
	for _, line := range strings.Split(remotes, "\n") {
		if strings.TrimSpace(line) == "fork" {
			hasFork = true
			break
		}
	}
	if hasFork {
		if _, err := gitClient.Remote("set-url", "fork", forkURL); err != nil {
			return fmt.Errorf("updating fork remote URL: %w", err)
		}
	} else {
		if _, err := gitClient.Remote("add", "fork", forkURL); err != nil {
			return fmt.Errorf("adding fork remote: %w", err)
		}
	}

	if err := gitClient.Checkout("-b", branchName); err != nil {
		return fmt.Errorf("creating branch: %w", err)
	}
	return nil
}

func stageChanges(
	cfg *config.Config, gitClient git.Client, tmpDir string,
) (commitSubject, commitBody string, err error) {
	commitSubject, commitBody, err = readCommitMessage(cfg)
	if err != nil {
		return "", "", err
	}
	if err := copyPRBody(cfg, tmpDir); err != nil {
		return "", "", err
	}

	// Stage only files that appear in the working tree diff (unstaged, staged,
	// and untracked). This avoids blindly staging credential files or other
	// artifacts dropped by action steps (e.g., gha-creds-*.json).
	changedFiles, err := git.ChangedFiles(gitClient)
	if err != nil {
		return "", "", fmt.Errorf("listing changed files: %w", err)
	}
	for _, f := range changedFiles {
		if err := gitClient.Add(f); err != nil {
			return "", "", fmt.Errorf("staging %s: %w", f, err)
		}
	}
	return commitSubject, commitBody, nil
}

func validateChanges(
	ctx context.Context,
	cfg *config.Config,
	runner claude.Runner,
	gitClient git.Client,
	tmpDir string,
	tracker *claude.UsageTracker,
) error {
	// Run security check on final staged changeset
	violations, err := security.Check(gitClient, cfg.BlockedPaths)
	if err != nil {
		return fmt.Errorf("security check: %w", err)
	}
	if len(violations) > 0 {
		for _, v := range violations {
			action.LogWarning(v)
		}
		return fmt.Errorf("security check failed: %d violation(s) found", len(violations))
	}

	stagedFiles, err := gitClient.Diff("--cached", "--name-only")
	if err != nil {
		return fmt.Errorf("checking staged changes: %w", err)
	}
	if strings.TrimSpace(stagedFiles) == "" {
		return fmt.Errorf("no changes to commit")
	}

	if err := aiSecurityReview(ctx, cfg, runner, gitClient, tmpDir, tracker); err != nil {
		return fmt.Errorf("AI security review failed: %w", err)
	}
	return nil
}

func commitAndPush(
	cfg *config.Config, gitClient git.Client, branchName, commitSubject, commitBody string,
) (title string, err error) {
	title = commitSubject
	if title == "" {
		return "", fmt.Errorf("no PR title available: ensure Claude writes .autosolve-commit-message")
	}

	commitMsg := title
	if commitBody != "" {
		commitMsg += "\n\n" + commitBody
	}
	commitMsg += "\n\n" + cfg.CommitSignature

	if err := gitClient.Commit(commitMsg); err != nil {
		return "", fmt.Errorf("committing: %w", err)
	}

	if err := gitClient.Push("--set-upstream", "fork", branchName, "--force"); err != nil {
		return "", fmt.Errorf("pushing to fork: %w", err)
	}
	return title, nil
}

func createPR(
	ctx context.Context, cfg *config.Config, ghClient github.Client, tmpDir, branchName, title string,
) (string, error) {
	prBody := buildPRBody(cfg, tmpDir)

	// Best-effort label creation: if the token lacks permission, warn and
	// continue — the PR is still created, just without the label.
	if cfg.PRLabels != "" {
		for _, label := range strings.Split(cfg.PRLabels, ",") {
			label = strings.TrimSpace(label)
			if label != "" {
				if err := ghClient.CreateLabel(ctx, cfg.PRTargetRepo, label); err != nil {
					action.LogWarning(fmt.Sprintf("failed creating label %q: %v", label, err))
				}
			}
		}
	}

	prURL, err := ghClient.CreatePR(ctx, github.PullRequestOptions{
		Repo:   cfg.PRTargetRepo,
		Head:   fmt.Sprintf("%s:%s", cfg.ForkOwner, branchName),
		Base:   cfg.PRBaseBranch,
		Title:  title,
		Body:   prBody,
		Labels: cfg.PRLabels,
		Draft:  cfg.PRDraft,
	})
	if err != nil {
		return "", fmt.Errorf("creating PR: %w", err)
	}

	action.LogNotice(fmt.Sprintf("PR created: %s", prURL))
	if err := action.SetOutput("pr_url", prURL); err != nil {
		return "", fmt.Errorf("setting output: %w", err)
	}
	if err := action.SetOutput("branch_name", branchName); err != nil {
		return "", fmt.Errorf("setting output: %w", err)
	}
	return prURL, nil
}

// commitMessagePath returns the scratch path where claude is told to
// write the commit message. Lives outside the repo so it can never be
// accidentally staged.
func commitMessagePath(cfg *config.Config) string {
	return filepath.Join(cfg.ScratchDir, "commit-message")
}

// prBodyPath returns the scratch path where claude is told to write the
// PR body. Lives outside the repo so it can never be accidentally staged.
func prBodyPath(cfg *config.Config) string {
	return filepath.Join(cfg.ScratchDir, "pr-body")
}

func readCommitMessage(cfg *config.Config) (subject, body string, err error) {
	path := commitMessagePath(cfg)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("reading commit message: %w", err)
	}
	// Fail hard: a stale file could interfere with later retry attempts.
	if err := os.Remove(path); err != nil {
		return "", "", fmt.Errorf("removing commit message file: %w", err)
	}

	lines := strings.SplitN(string(data), "\n", 3)
	if len(lines) > 0 {
		subject = strings.TrimSpace(lines[0])
	}
	if len(lines) > 2 {
		body = strings.TrimSpace(lines[2])
	}
	return subject, body, nil
}

func copyPRBody(cfg *config.Config, tmpDir string) error {
	path := prBodyPath(cfg)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading PR body: %w", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "autosolve-pr-body"), data, 0644); err != nil {
		return fmt.Errorf("copying PR body: %w", err)
	}
	// Fail hard: a stale file could interfere with later retry attempts.
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("removing PR body file: %w", err)
	}
	return nil
}

func buildPRBody(cfg *config.Config, tmpDir string) string {
	var body string
	if data, err := os.ReadFile(filepath.Join(tmpDir, "autosolve-pr-body")); err == nil {
		body = string(data)
	}
	body += "\n\n" + cfg.PRFooter
	return body
}

func extractSummary(resultText, marker string) string {
	var lines []string
	for _, line := range strings.Split(resultText, "\n") {
		if !strings.HasPrefix(line, marker) {
			lines = append(lines, line)
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

const securityReviewPrompt = `You are a security reviewer. Your ONLY task is to review the staged
changes for sensitive content that should NOT be committed to a repository.

Use the available tools to inspect the staged changes. Start by running
"git diff --cached" to see what will be committed. You may skip files
that are clearly auto-generated (contain "Code generated", "DO NOT EDIT",
or similar markers).

Look for:
- Credentials, API keys, tokens, passwords (hardcoded or in config)
- Private keys, certificates, keystores
- Cloud provider credential files (e.g., gha-creds-*.json, service account keys)
- .env files or environment variable files containing secrets
- Database connection strings with embedded passwords
- Any other secrets or sensitive data

You MUST review all staged changes before producing your result.

**OUTPUT REQUIREMENT**: End your response with exactly one of:
SECURITY_REVIEW - SUCCESS (if no sensitive content found)
SECURITY_REVIEW - FAILED (if any sensitive content found)

If you find sensitive content, describe each finding by file and line number
before the FAILED marker. NEVER include the actual secret values in your
response — your output is logged.`

// aiSecurityReview runs a Claude invocation to scan the staged diff for
// sensitive content that pattern matching might miss. Claude uses tools
// to read the diffs itself, so untrusted code is never injected into the
// prompt — mitigating prompt injection from attacker-controlled content.
func aiSecurityReview(
	ctx context.Context,
	cfg *config.Config,
	runner claude.Runner,
	gitClient git.Client,
	tmpDir string,
	tracker *claude.UsageTracker,
) error {
	action.LogInfo("Running AI security review on staged changes...")

	// Quick check: anything staged?
	stagedOutput, err := gitClient.Diff("--cached", "--name-only")
	if err != nil {
		return fmt.Errorf("listing staged files: %w", err)
	}
	if strings.TrimSpace(stagedOutput) == "" {
		return nil
	}

	outputFile := filepath.Join(tmpDir, "security-review-claude-output.json")
	result, err := runner.Run(ctx, claude.RunOptions{
		Model:        cfg.SecurityReviewModel(),
		AllowedTools: "Bash(git diff:*),Bash(git show:*),Read,Grep,Glob",
		MaxTurns:     100,
		Prompt:       securityReviewPrompt,
		OutputFile:   outputFile,
		Sandbox: &claude.SandboxOptions{
			WorkingDir: cfg.WorkingDir,
			ScratchDir: cfg.ScratchDir,
			ReadPaths:  cfg.ReadPaths,
		},
	})
	if err != nil {
		// Best-effort unstage; safe to continue because the return
		// below stops execution before any push can occur.
		if resetErr := gitClient.ResetHead(); resetErr != nil {
			action.LogWarning(fmt.Sprintf("failed to reset staged changes: %v", resetErr))
		}
		return fmt.Errorf("AI security review: %w", err)
	}
	tracker.Record("security review", result.Usage)

	resultText, positive, err := claude.ExtractResult(outputFile, "SECURITY_REVIEW")
	if err != nil {
		if resetErr := gitClient.ResetHead(); resetErr != nil {
			action.LogWarning(fmt.Sprintf("failed to reset staged changes: %v", resetErr))
		}
		return fmt.Errorf("AI security review: %w", err)
	}

	if !positive {
		action.LogWarning("AI security review found sensitive content:")
		action.LogWarning(resultText)
		// Best-effort unstage; safe to continue because the return
		// below stops execution before any push can occur.
		if err := gitClient.ResetHead(); err != nil {
			action.LogWarning(fmt.Sprintf("failed to reset staged changes: %v", err))
		}
		return fmt.Errorf("sensitive content detected in staged changes")
	}

	action.LogNotice("AI security review passed")
	return nil
}

func writeOutputs(
	status, prURL, branchName, resultText string, tracker *claude.UsageTracker,
) error {
	summary := extractSummary(resultText, "IMPLEMENTATION_RESULT")
	summary = action.TruncateOutput(200, summary)

	if err := action.SetOutput("status", status); err != nil {
		return fmt.Errorf("setting output: %w", err)
	}
	if err := action.SetOutput("pr_url", prURL); err != nil {
		return fmt.Errorf("setting output: %w", err)
	}
	if err := action.SetOutput("branch_name", branchName); err != nil {
		return fmt.Errorf("setting output: %w", err)
	}
	if err := action.SetOutputMultiline("summary", summary); err != nil {
		return fmt.Errorf("setting output: %w", err)
	}
	if err := action.SetOutputMultiline("result", resultText); err != nil {
		return fmt.Errorf("setting output: %w", err)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "## Autosolve Implementation\n**Status:** %s\n", status)
	if prURL != "" {
		fmt.Fprintf(&sb, "**PR:** %s\n", prURL)
	}
	if branchName != "" {
		fmt.Fprintf(&sb, "**Branch:** `%s`\n", branchName)
	}
	if summary != "" {
		fmt.Fprintf(&sb, "### Summary\n%s\n", summary)
	}
	if tracker != nil {
		// Load usage from earlier steps (e.g. assess) so the table is combined
		tracker.Load()
		if saveErr := tracker.Save(); saveErr != nil {
			action.LogWarning(fmt.Sprintf("failed to save usage summary: %v", saveErr))
		}
		total := tracker.Total()
		action.LogInfo(fmt.Sprintf("Total usage: input=%d output=%d cost=$%.4f",
			total.InputTokens, total.OutputTokens, total.CostUSD))
	}
	if err := action.WriteStepSummary(sb.String()); err != nil {
		return fmt.Errorf("writing step summary: %w", err)
	}

	return nil
}
