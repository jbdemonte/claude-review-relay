package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jbd/claude-reviewer/internal/apperr"
	"github.com/jbd/claude-reviewer/internal/reviewer"
	"github.com/jbd/claude-reviewer/internal/session"
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
	found := make(map[string]*mcpsdk.Tool, len(result.Tools))
	for _, tool := range result.Tools {
		found[tool.Name] = tool
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
	}
	for _, name := range []string{"review_diff", "start_review", "start_continue_review", "get_review_status"} {
		if found[name] == nil {
			t.Fatalf("tool %s not found", name)
		}
	}
	if found["get_review_status"].Annotations == nil || !found["get_review_status"].Annotations.ReadOnlyHint {
		t.Fatalf("get_review_status annotations=%+v", found["get_review_status"].Annotations)
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

func TestMetadataAndStatusToolsRoundTripThroughMCP(t *testing.T) {
	ctx := context.Background()
	store := session.NewJSONStore(filepath.Join(t.TempDir(), "sessions.json"))
	now := time.Now().UTC()
	record := session.ReviewSession{
		ReviewID: "R", ClaudeSessionID: "A", RepositoryPath: "/repo", Goal: "test", BaseRef: "HEAD",
		Status: session.ReviewStatusOpen, ResponseSequence: 1,
		LastResponse:     json.RawMessage(`{"verdict":"approve","summary":"ok","findings":[],"missing_tests":[]}`),
		LastErrorDetails: map[string]any{"internal": "must only appear in status"}, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Create(ctx, record); err != nil {
		t.Fatal(err)
	}
	server := New(reviewer.NewService(store, nil, nil, "fable", "opus", "max", 20, 240, nil))
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

	getResult, err := clientSession.CallTool(ctx, &mcpsdk.CallToolParams{Name: "get_review", Arguments: map[string]any{"review_id": "R"}})
	if err != nil || getResult.IsError {
		t.Fatalf("get_review result=%+v err=%v", getResult, err)
	}
	getJSON, err := json.Marshal(getResult.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(getJSON), "last_response") || strings.Contains(string(getJSON), "last_error_details") {
		t.Fatalf("get_review leaked internal payload: %s", getJSON)
	}

	listResult, err := clientSession.CallTool(ctx, &mcpsdk.CallToolParams{Name: "list_reviews", Arguments: map[string]any{}})
	if err != nil || listResult.IsError {
		t.Fatalf("list_reviews result=%+v err=%v", listResult, err)
	}
	listJSON, err := json.Marshal(listResult.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(listJSON), "last_response") || strings.Contains(string(listJSON), "last_error_details") {
		t.Fatalf("list_reviews leaked internal payload: %s", listJSON)
	}

	statusResult, err := clientSession.CallTool(ctx, &mcpsdk.CallToolParams{Name: "get_review_status", Arguments: map[string]any{"review_id": "R"}})
	if err != nil || statusResult.IsError {
		t.Fatalf("get_review_status result=%+v err=%v", statusResult, err)
	}
	statusJSON, err := json.Marshal(statusResult.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(statusJSON), `"response_sequence":1`) || !strings.Contains(string(statusJSON), `"verdict":"approve"`) {
		t.Fatalf("status payload=%s", statusJSON)
	}
}
