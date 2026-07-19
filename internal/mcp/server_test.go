package mcp

import (
	"errors"
	"strings"
	"testing"

	"github.com/jbd/claude-reviewer/internal/apperr"
)

func TestSafeErrorIsStructuredAndHidesCause(t *testing.T) {
	err := safeError(apperr.Wrap("claude_failed", "Claude a échoué.", errors.New("secret process detail"), map[string]any{"attempt": 1}))
	text := err.Error()
	if !strings.Contains(text, `"code":"claude_failed"`) || !strings.Contains(text, `"details":{"attempt":1}`) {
		t.Fatalf("error=%s", text)
	}
	if strings.Contains(text, "secret process detail") {
		t.Fatalf("cause leaked: %s", text)
	}
}

func TestToolAnnotations(t *testing.T) {
	read := readOnlyAnnotations("read")
	if !read.ReadOnlyHint || read.OpenWorldHint == nil || *read.OpenWorldHint {
		t.Fatalf("read annotations=%+v", read)
	}
	close := mutatingAnnotations("close", true, false)
	if close.ReadOnlyHint || close.DestructiveHint == nil || !*close.DestructiveHint {
		t.Fatalf("close annotations=%+v", close)
	}
}
