package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jbd/claude-reviewer/internal/apperr"
	"github.com/jbd/claude-reviewer/internal/reviewer"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSafeErrorIsStructuredAndHidesCause(t *testing.T) {
	err := safeError(apperr.Wrap("claude_failed", "Claude failed.", errors.New("secret process detail"), map[string]any{"attempt": 1}))
	text := err.Error()
	if !strings.Contains(text, `"code":"claude_failed"`) || !strings.Contains(text, `"details":{"attempt":1}`) {
		t.Fatalf("error=%s", text)
	}
	if strings.Contains(text, "secret process detail") {
		t.Fatalf("cause leaked: %s", text)
	}
}

func TestReviewDiffSchemaExposesTimeoutAndLiteralPathScope(t *testing.T) {
	ctx := context.Background()
	server := New(&reviewer.Service{})
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := server.sdk.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "1"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()
	result, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range result.Tools {
		if tool.Name != "review_diff" {
			continue
		}
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"timeout_seconds", "include_paths", "exclude_paths"} {
			if !strings.Contains(string(raw), `"`+name+`"`) {
				t.Fatalf("review_diff schema does not contain %s: %s", name, raw)
			}
		}
		return
	}
	t.Fatal("review_diff tool not found")
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
