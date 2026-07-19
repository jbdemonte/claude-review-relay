package mcp

import (
	"context"
	"encoding/json"

	"github.com/jbd/claude-reviewer/internal/apperr"
	"github.com/jbd/claude-reviewer/internal/reviewer"
	"github.com/jbd/claude-reviewer/internal/session"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const Version = "0.1.0"

type Server struct{ sdk *mcpsdk.Server }

type reviewsOutput struct {
	Reviews []session.ReviewSession `json:"reviews"`
}

func New(service *reviewer.Service) *Server {
	sdk := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "claude-reviewer", Title: "Claude Reviewer", Version: Version}, &mcpsdk.ServerOptions{Instructions: "Independent read-only code review through persistent Claude Code sessions."})
	mcpsdk.AddTool(sdk, &mcpsdk.Tool{Name: "review_diff", Description: "Start a new independent review of the server-computed Git diff and persist its Claude session.", Annotations: mutatingAnnotations("Start Claude review", false, true)}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, in reviewer.ReviewDiffInput) (*mcpsdk.CallToolResult, reviewer.ReviewOutput, error) {
		out, err := service.ReviewDiff(ctx, in)
		return nil, out, safeError(err)
	})
	mcpsdk.AddTool(sdk, &mcpsdk.Tool{Name: "continue_review", Description: "Resume exactly the same Claude conversation using the persisted explicit session_id.", Annotations: mutatingAnnotations("Continue Claude review", false, true)}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, in reviewer.ContinueReviewInput) (*mcpsdk.CallToolResult, reviewer.ReviewOutput, error) {
		out, err := service.ContinueReview(ctx, in)
		return nil, out, safeError(err)
	})
	mcpsdk.AddTool(sdk, &mcpsdk.Tool{Name: "get_review", Description: "Return local metadata for one persisted review without calling Claude.", Annotations: readOnlyAnnotations("Get review metadata")}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, in reviewer.GetReviewInput) (*mcpsdk.CallToolResult, session.ReviewSession, error) {
		out, err := service.GetReview(ctx, in.ReviewID)
		return nil, out, safeError(err)
	})
	mcpsdk.AddTool(sdk, &mcpsdk.Tool{Name: "list_reviews", Description: "List persisted reviews, newest first, optionally filtered by repository and status.", Annotations: readOnlyAnnotations("List reviews")}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, in reviewer.ListReviewsInput) (*mcpsdk.CallToolResult, reviewsOutput, error) {
		out, err := service.ListReviews(ctx, in)
		return nil, reviewsOutput{Reviews: out}, safeError(err)
	})
	mcpsdk.AddTool(sdk, &mcpsdk.Tool{Name: "close_review", Description: "Close a review or delete only its local association. Native Claude session data is not deleted in V1.", Annotations: mutatingAnnotations("Close review", true, false)}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, in reviewer.CloseReviewInput) (*mcpsdk.CallToolResult, reviewer.CloseOutput, error) {
		out, err := service.CloseReview(ctx, in)
		return nil, out, safeError(err)
	})
	return &Server{sdk: sdk}
}

func readOnlyAnnotations(title string) *mcpsdk.ToolAnnotations {
	closed := false
	return &mcpsdk.ToolAnnotations{Title: title, ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &closed}
}

func mutatingAnnotations(title string, destructive, openWorld bool) *mcpsdk.ToolAnnotations {
	return &mcpsdk.ToolAnnotations{Title: title, DestructiveHint: &destructive, OpenWorldHint: &openWorld}
}

func (s *Server) Run(ctx context.Context) error { return s.sdk.Run(ctx, &mcpsdk.StdioTransport{}) }

type publicError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}

func (e publicError) Error() string { b, _ := json.Marshal(e); return string(b) }
func safeError(err error) error {
	if err == nil {
		return nil
	}
	e := apperr.From(err)
	return publicError{Code: e.Code, Message: e.Message, Details: e.Details}
}
