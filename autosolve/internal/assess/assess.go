// Package assess implements the assessment phase of autosolve.
package assess

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/cockroachdb/actions/autosolve/internal/action"
	"github.com/cockroachdb/actions/autosolve/internal/claude"
	"github.com/cockroachdb/actions/autosolve/internal/config"
	"github.com/cockroachdb/actions/autosolve/internal/prompt"
)

// Run executes the assessment phase.
func Run(ctx context.Context, cfg *config.Config, runner claude.Runner, tmpDir string) error {
	// Build prompt
	promptFile, err := prompt.Build(cfg, tmpDir)
	if err != nil {
		return fmt.Errorf("building prompt: %w", err)
	}

	action.LogInfo(fmt.Sprintf("Running assessment with model: %s", cfg.Model))

	outputFile := filepath.Join(tmpDir, "assess-claude-output.json")

	var tracker claude.UsageTracker

	// Build allowed tools: read-only plus printenv for each context var
	// so Claude can read them without full Bash access.
	tools := []string{"Read", "Grep", "Glob"}
	for _, v := range cfg.ContextVars {
		tools = append(tools, fmt.Sprintf("Bash(printenv %s)", v))
	}
	allowedTools := strings.Join(tools, ",")

	// Invoke Claude in read-only mode
	if cfg.LogLevel != "error" {
		action.BeginLogGroup("Claude output: assess")
	}
	result, err := runner.Run(ctx, claude.RunOptions{
		Model:        cfg.Model,
		AllowedTools: allowedTools,
		MaxTurns:     30,
		PromptFile:   promptFile,
		OutputFile:   outputFile,
		ContextVars:  cfg.ContextVars,
		LogLevel:     cfg.LogLevel,
		Sandbox: &claude.SandboxOptions{
			WorkingDir: cfg.WorkingDir,
			ScratchDir: cfg.ScratchDir,
			ReadPaths:  cfg.ReadPaths,
		},
	})
	if cfg.LogLevel != "error" {
		action.EndLogGroup()
	}
	claude.LogResult(&tracker, result, "assess")
	if saveErr := tracker.Save(); saveErr != nil {
		action.LogWarning(fmt.Sprintf("failed to save usage summary: %v", saveErr))
	}
	if err != nil {
		return fmt.Errorf("running claude: %w", err)
	}

	// Extract result
	resultText, positive, err := claude.ExtractResult(outputFile, "ASSESSMENT_RESULT")
	if err != nil {
		action.LogError(fmt.Sprintf("No assessment result found in Claude output: %v", err))
		if setErr := action.SetOutput("assessment", "ERROR"); setErr != nil {
			return fmt.Errorf("setting output: %w", setErr)
		}
		return fmt.Errorf("extracting assessment result: %w", err)
	}

	action.LogInfo(resultText)

	var assessment string
	if positive {
		assessment = "PROCEED"
		action.LogNotice("Assessment: PROCEED")
	} else if strings.Contains(resultText, "ASSESSMENT_RESULT - SKIP") {
		assessment = "SKIP"
		action.LogNotice("Assessment: SKIP")
	} else {
		action.LogError("Assessment result did not contain a valid PROCEED or SKIP marker")
		if setErr := action.SetOutput("assessment", "ERROR"); setErr != nil {
			return fmt.Errorf("setting output: %w", setErr)
		}
		return fmt.Errorf("invalid assessment result")
	}

	// Write outputs
	summary := extractSummary(resultText, "ASSESSMENT_RESULT")
	summary = action.TruncateOutput(200, summary)

	if err := action.SetOutput("assessment", assessment); err != nil {
		return fmt.Errorf("setting output: %w", err)
	}
	if err := action.SetOutputMultiline("summary", summary); err != nil {
		return fmt.Errorf("setting output: %w", err)
	}
	if err := action.SetOutputMultiline("result", resultText); err != nil {
		return fmt.Errorf("setting output: %w", err)
	}
	if err := action.WriteStepSummary(formatStepSummary(assessment, summary)); err != nil {
		return fmt.Errorf("writing step summary: %w", err)
	}

	return nil
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

func formatStepSummary(assessment, summary string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Autosolve Assessment\n**Result:** %s\n", assessment)
	if summary != "" {
		fmt.Fprintf(&b, "### Summary\n%s\n", summary)
	}
	return b.String()
}
