package reviewer

import (
	_ "embed"
	"fmt"
	"strings"
)

//go:embed prompts/reviewer.md
var SystemPrompt string

type InitialPromptInput struct {
	Goal, BaseRef, Diff, AdditionalContext, TestResults string
	ReviewFocus, UntrackedFiles, ExcludedFiles          []string
	RedactionCount                                      int
}

func InitialPrompt(in InitialPromptInput) string {
	return fmt.Sprintf(`Perform an independent review of the following change.

GOAL
%s

GIT BASE
%s

REVIEW FOCUS
%s

SERVER-COMPUTED DIFF
%s

UNTRACKED FILES (contents not included)
%s

SENSITIVE CONTENT
Excluded files: %s
Redacted values: %d

TEST RESULTS PROVIDED BY THE AUTHOR
%s

ADDITIONAL CONTEXT
%s

Read the necessary surrounding code in read-only mode. Return only the requested structured response.`,
		none(in.Goal), none(in.BaseRef), list(in.ReviewFocus), none(in.Diff), list(in.UntrackedFiles), list(in.ExcludedFiles), in.RedactionCount, none(in.TestResults), none(in.AdditionalContext))
}

func ContinuePrompt(message, diff, testResults string, untracked, excluded []string, redactions int) string {
	var b strings.Builder
	b.WriteString("Continue the same review. Preserve the context and previous finding IDs. Verify fixes before updating previous_findings.\n\nNEW MESSAGE\n")
	b.WriteString(none(message))
	if diff != "" || len(untracked) > 0 || len(excluded) > 0 {
		fmt.Fprintf(&b, "\n\nRECOMPUTED CURRENT DIFF\n%s\n\nUNTRACKED FILES\n%s\n\nSENSITIVE CONTENT\nExcluded files: %s\nRedacted values: %d", none(diff), list(untracked), list(excluded), redactions)
	}
	if testResults != "" {
		b.WriteString("\n\nNEW TEST RESULTS PROVIDED\n" + testResults)
	}
	b.WriteString("\n\nReturn only the requested structured response.")
	return b.String()
}

func none(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(none)"
	}
	return s
}
func list(v []string) string {
	if len(v) == 0 {
		return "(none)"
	}
	return strings.Join(v, ", ")
}
