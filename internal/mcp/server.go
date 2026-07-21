package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/jbd/claude-reviewer/internal/apperr"
	"github.com/jbd/claude-reviewer/internal/reviewer"
	"github.com/jbd/claude-reviewer/internal/session"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const Version = "0.3.0"

type Server struct{ sdk *mcpsdk.Server }

type reviewMetadata struct {
	ReviewID          string               `json:"review_id"`
	ClaudeSessionID   string               `json:"claude_session_id,omitempty"`
	RepositoryPath    string               `json:"repository_path"`
	Goal              string               `json:"goal"`
	BaseRef           string               `json:"base_ref"`
	IncludePaths      []string             `json:"include_paths,omitempty"`
	ExcludePaths      []string             `json:"exclude_paths,omitempty"`
	HeadSHAAtStart    string               `json:"head_sha_at_start"`
	Model             string               `json:"model"`
	FallbackModel     string               `json:"fallback_model,omitempty"`
	Effort            string               `json:"effort,omitempty"`
	MaxTurns          int                  `json:"max_turns"`
	TimeoutSeconds    int                  `json:"timeout_seconds,omitempty"`
	Status            session.ReviewStatus `json:"status"`
	ActiveOperation   string               `json:"active_operation,omitempty"`
	ResponseSequence  int                  `json:"response_sequence"`
	LastErrorCode     string               `json:"last_error_code,omitempty"`
	LastExcludedFiles []string             `json:"last_excluded_files,omitempty"`
	RedactionCount    int                  `json:"redaction_count"`
	CreatedAt         time.Time            `json:"created_at"`
	UpdatedAt         time.Time            `json:"updated_at"`
}

type reviewsOutput struct {
	Reviews []reviewMetadata `json:"reviews"`
}

func New(service *reviewer.Service) *Server {
	sdk := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "claude-reviewer", Title: "Claude Reviewer", Version: Version}, &mcpsdk.ServerOptions{Instructions: "Independent read-only code review through persistent Claude Code sessions."})
	mcpsdk.AddTool(sdk, &mcpsdk.Tool{Name: "review_diff", Description: "Start a new independent review of the server-computed Git diff and persist its Claude session.", Annotations: mutatingAnnotations("Start Claude review", false, true)}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in reviewer.ReviewDiffInput) (*mcpsdk.CallToolResult, reviewer.ReviewOutput, error) {
		stopProgress := startProgress(ctx, req, "Claude review")
		defer stopProgress()
		out, err := service.ReviewDiff(ctx, in)
		return nil, out, safeError(err)
	})
	mcpsdk.AddTool(sdk, &mcpsdk.Tool{Name: "continue_review", Description: "Resume exactly the same Claude conversation using the persisted explicit session_id.", Annotations: mutatingAnnotations("Continue Claude review", false, true)}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in reviewer.ContinueReviewInput) (*mcpsdk.CallToolResult, reviewer.ReviewOutput, error) {
		stopProgress := startProgress(ctx, req, "Claude review continuation")
		defer stopProgress()
		out, err := service.ContinueReview(ctx, in)
		return nil, out, safeError(err)
	})
	mcpsdk.AddTool(sdk, &mcpsdk.Tool{Name: "start_review", Description: "Persist and start a review in the background, returning immediately with a review_id for polling.", Annotations: mutatingAnnotations("Start background Claude review", false, true)}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, in reviewer.ReviewDiffInput) (*mcpsdk.CallToolResult, reviewer.AsyncStartOutput, error) {
		out, err := service.StartReview(ctx, in)
		return nil, out, safeError(err)
	})
	mcpsdk.AddTool(sdk, &mcpsdk.Tool{Name: "start_continue_review", Description: "Resume the persisted explicit Claude session in the background and return immediately.", Annotations: mutatingAnnotations("Start background Claude continuation", false, true)}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, in reviewer.ContinueReviewInput) (*mcpsdk.CallToolResult, reviewer.AsyncStartOutput, error) {
		out, err := service.StartContinueReview(ctx, in)
		return nil, out, safeError(err)
	})
	mcpsdk.AddTool(sdk, &mcpsdk.Tool{Name: "get_review_status", Description: "Poll a background review and return its persisted status, latest structured response, or actionable error.", Annotations: readOnlyAnnotations("Get background review status")}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, in reviewer.GetReviewInput) (*mcpsdk.CallToolResult, reviewer.ReviewStatusOutput, error) {
		out, err := service.GetReviewStatus(ctx, in.ReviewID)
		return nil, out, safeError(err)
	})
	mcpsdk.AddTool(sdk, &mcpsdk.Tool{Name: "get_review", Description: "Return local metadata for one persisted review without calling Claude.", Annotations: readOnlyAnnotations("Get review metadata")}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, in reviewer.GetReviewInput) (*mcpsdk.CallToolResult, reviewMetadata, error) {
		out, err := service.GetReview(ctx, in.ReviewID)
		return nil, metadata(out), safeError(err)
	})
	mcpsdk.AddTool(sdk, &mcpsdk.Tool{Name: "list_reviews", Description: "List persisted reviews, newest first, optionally filtered by repository and status.", Annotations: readOnlyAnnotations("List reviews")}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, in reviewer.ListReviewsInput) (*mcpsdk.CallToolResult, reviewsOutput, error) {
		out, err := service.ListReviews(ctx, in)
		reviews := make([]reviewMetadata, 0, len(out))
		for _, record := range out {
			reviews = append(reviews, metadata(record))
		}
		return nil, reviewsOutput{Reviews: reviews}, safeError(err)
	})
	mcpsdk.AddTool(sdk, &mcpsdk.Tool{Name: "close_review", Description: "Close a review or delete only its local association. Native Claude session data is not deleted in V1.", Annotations: mutatingAnnotations("Close review", true, false)}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, in reviewer.CloseReviewInput) (*mcpsdk.CallToolResult, reviewer.CloseOutput, error) {
		out, err := service.CloseReview(ctx, in)
		return nil, out, safeError(err)
	})
	return &Server{sdk: sdk}
}

func metadata(record session.ReviewSession) reviewMetadata {
	return reviewMetadata{
		ReviewID: record.ReviewID, ClaudeSessionID: record.ClaudeSessionID,
		RepositoryPath: record.RepositoryPath, Goal: record.Goal, BaseRef: record.BaseRef,
		IncludePaths: append([]string(nil), record.IncludePaths...), ExcludePaths: append([]string(nil), record.ExcludePaths...),
		HeadSHAAtStart: record.HeadSHAAtStart, Model: record.Model, FallbackModel: record.FallbackModel,
		Effort: record.Effort, MaxTurns: record.MaxTurns, TimeoutSeconds: record.TimeoutSeconds,
		Status: record.Status, ActiveOperation: record.ActiveOperation, ResponseSequence: record.ResponseSequence,
		LastErrorCode: record.LastErrorCode, LastExcludedFiles: append([]string(nil), record.LastExcludedFiles...),
		RedactionCount: record.LastRedactionCount, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}
}

func readOnlyAnnotations(title string) *mcpsdk.ToolAnnotations {
	closed := false
	return &mcpsdk.ToolAnnotations{Title: title, ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &closed}
}

func mutatingAnnotations(title string, destructive, openWorld bool) *mcpsdk.ToolAnnotations {
	return &mcpsdk.ToolAnnotations{Title: title, DestructiveHint: &destructive, OpenWorldHint: &openWorld}
}

func (s *Server) Run(ctx context.Context) error { return s.sdk.Run(ctx, &mcpsdk.StdioTransport{}) }

func startProgress(ctx context.Context, req *mcpsdk.CallToolRequest, operation string) func() {
	if req == nil || req.Session == nil || req.Params == nil {
		return func() {}
	}
	token := req.Params.GetProgressToken()
	if token == nil {
		return func() {}
	}
	progressCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		started := time.Now()
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		progress := func(message string) {
			_ = req.Session.NotifyProgress(progressCtx, &mcpsdk.ProgressNotificationParams{
				ProgressToken: token,
				Progress:      time.Since(started).Seconds(),
				Message:       message,
			})
		}
		progress(operation + " started")
		for {
			select {
			case <-progressCtx.Done():
				return
			case <-ticker.C:
				progress(fmt.Sprintf("%s is still running (%s elapsed)", operation, time.Since(started).Round(time.Second)))
			}
		}
	}()
	return func() {
		cancel()
		wg.Wait()
	}
}

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
