package reviewer

import (
	"strings"
	"testing"
)

func TestContinuePromptReportsAnEmptyRefreshedDiff(t *testing.T) {
	prompt := ContinuePrompt("verify", "", "", []string{"internal"}, nil, nil, nil, 0, true)
	if !strings.Contains(prompt, "RECOMPUTED CURRENT DIFF\n(none)") {
		t.Fatalf("prompt=%s", prompt)
	}
	withoutRefresh := ContinuePrompt("verify", "", "", nil, nil, nil, nil, 0, false)
	if strings.Contains(withoutRefresh, "RECOMPUTED CURRENT DIFF") {
		t.Fatalf("prompt without refresh=%s", withoutRefresh)
	}
}
