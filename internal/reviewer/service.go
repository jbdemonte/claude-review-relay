package reviewer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/jbd/claude-reviewer/internal/apperr"
	"github.com/jbd/claude-reviewer/internal/claude"
	"github.com/jbd/claude-reviewer/internal/config"
	gitservice "github.com/jbd/claude-reviewer/internal/git"
	"github.com/jbd/claude-reviewer/internal/security"
	"github.com/jbd/claude-reviewer/internal/session"
)

type Service struct {
	Store                 session.SessionStore
	Git                   gitservice.GitService
	Claude                claude.Client
	DefaultModel          string
	DefaultFallbackModel  string
	DefaultEffort         string
	DefaultMaxTurns       int
	DefaultTimeoutSeconds int
	Now                   func() time.Time
	Logger                *slog.Logger
	locks                 sessionLocks
}

type ReviewDiffInput struct {
	RepositoryPath    string   `json:"repository_path" jsonschema:"absolute path of the Git repository"`
	Goal              string   `json:"goal" jsonschema:"functional goal of the implemented change"`
	BaseRef           string   `json:"base_ref,omitempty" jsonschema:"Git base reference, defaults to HEAD"`
	ReviewFocus       []string `json:"review_focus,omitempty"`
	IncludePaths      []string `json:"include_paths,omitempty" jsonschema:"repository-relative files or directories to include in the server-computed diff"`
	ExcludePaths      []string `json:"exclude_paths,omitempty" jsonschema:"repository-relative files or directories to exclude from the server-computed diff"`
	AdditionalContext string   `json:"additional_context,omitempty"`
	TestResults       string   `json:"test_results,omitempty"`
	Model             string   `json:"model,omitempty"`
	FallbackModel     string   `json:"fallback_model,omitempty"`
	Effort            string   `json:"effort,omitempty"`
	MaxTurns          int      `json:"max_turns,omitempty"`
	TimeoutSeconds    int      `json:"timeout_seconds,omitempty" jsonschema:"Claude subprocess timeout in seconds, from 1 to 1200; cannot extend the MCP client's own deadline"`
}

type ContinueReviewInput struct {
	ReviewID       string `json:"review_id"`
	Message        string `json:"message"`
	RefreshDiff    bool   `json:"refresh_diff,omitempty"`
	TestResults    string `json:"test_results,omitempty"`
	RepositoryPath string `json:"repository_path,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"Claude subprocess timeout in seconds, from 1 to 1200; cannot extend the MCP client's own deadline"`
}

type GetReviewInput struct {
	ReviewID string `json:"review_id"`
}
type ListReviewsInput struct {
	RepositoryPath string               `json:"repository_path,omitempty"`
	Status         session.ReviewStatus `json:"status,omitempty"`
}
type CloseReviewInput struct {
	ReviewID            string `json:"review_id"`
	DeleteClaudeSession bool   `json:"delete_claude_session,omitempty"`
}

type ReviewOutput struct {
	ReviewID        string         `json:"review_id"`
	ClaudeSessionID string         `json:"claude_session_id"`
	Response        ReviewResponse `json:"response"`
	ExcludedFiles   []string       `json:"excluded_files,omitempty"`
	RedactionCount  int            `json:"redaction_count"`
}

type CloseOutput struct {
	ReviewID                string `json:"review_id"`
	Status                  string `json:"status"`
	LocalAssociationDeleted bool   `json:"local_association_deleted"`
}

func NewService(store session.SessionStore, git gitservice.GitService, client claude.Client, model, fallbackModel, effort string, maxTurns, timeoutSeconds int, logger *slog.Logger) *Service {
	return &Service{Store: store, Git: git, Claude: client, DefaultModel: model, DefaultFallbackModel: fallbackModel, DefaultEffort: effort, DefaultMaxTurns: maxTurns, DefaultTimeoutSeconds: timeoutSeconds, Logger: logger, Now: time.Now, locks: sessionLocks{values: map[string]bool{}}}
}

func (s *Service) ReviewDiff(ctx context.Context, in ReviewDiffInput) (ReviewOutput, error) {
	started := time.Now()
	if in.RepositoryPath == "" || in.Goal == "" {
		return ReviewOutput{}, apperr.New("invalid_request", "repository_path and goal are required.", nil)
	}
	reviewID, err := newUUID()
	if err != nil {
		return ReviewOutput{}, apperr.Wrap("storage_error", "Failed to generate the review identifier.", err, nil)
	}
	unlock, ok := s.locks.try(reviewID)
	if !ok {
		return ReviewOutput{}, apperr.New("review_busy", "This review is already in use.", nil)
	}
	defer unlock()
	root, err := s.Git.Root(ctx, in.RepositoryPath)
	if err != nil {
		return ReviewOutput{}, mapError(err)
	}
	if in.BaseRef == "" {
		in.BaseRef = "HEAD"
	}
	diff, untracked, excluded, redactions, err := s.prepareDiff(ctx, root, in.BaseRef, in.IncludePaths, in.ExcludePaths)
	if err != nil {
		return ReviewOutput{}, err
	}
	if diff == "" && len(untracked) == 0 {
		details := map[string]any{"include_paths": in.IncludePaths, "exclude_paths": in.ExcludePaths}
		if len(excluded) > 0 {
			details["sensitive_excluded_files"] = excluded
			return ReviewOutput{}, apperr.New("empty_review_scope", "The selected path scope contains only files excluded by the sensitive-content policy.", details)
		}
		return ReviewOutput{}, apperr.New("empty_review_scope", "The selected path scope contains no tracked diff or untracked files.", details)
	}
	head, err := s.Git.HeadSHA(ctx, root)
	if err != nil {
		return ReviewOutput{}, mapError(err)
	}
	if len(in.ReviewFocus) == 0 {
		in.ReviewFocus = []string{"correctness", "regressions", "architecture", "performance", "security", "tests"}
	}
	if in.Model == "" {
		in.Model = s.DefaultModel
	}
	if in.FallbackModel == "" {
		in.FallbackModel = s.DefaultFallbackModel
	}
	if in.Effort == "" {
		in.Effort = s.DefaultEffort
	}
	if !config.ValidEffort(in.Effort) {
		return ReviewOutput{}, apperr.New("invalid_request", "effort must be low, medium, high, xhigh, or max.", map[string]any{"effort": in.Effort})
	}
	if in.MaxTurns <= 0 {
		in.MaxTurns = s.DefaultMaxTurns
	}
	if err := s.resolveTimeout(&in.TimeoutSeconds); err != nil {
		return ReviewOutput{}, err
	}
	now := s.Now().UTC()
	record := session.ReviewSession{ReviewID: reviewID, RepositoryPath: root, Goal: in.Goal, BaseRef: in.BaseRef, IncludePaths: append([]string(nil), in.IncludePaths...), ExcludePaths: append([]string(nil), in.ExcludePaths...), HeadSHAAtStart: head, Model: in.Model, FallbackModel: in.FallbackModel, Effort: in.Effort, MaxTurns: in.MaxTurns, TimeoutSeconds: in.TimeoutSeconds, Status: session.ReviewStatusPending, CreatedAt: now, UpdatedAt: now}
	if err := s.Store.Create(ctx, record); err != nil {
		return ReviewOutput{}, apperr.Wrap("storage_error", "Failed to persist the pending review.", err, nil)
	}
	prompt := InitialPrompt(InitialPromptInput{Goal: in.Goal, BaseRef: in.BaseRef, Diff: diff, AdditionalContext: in.AdditionalContext, TestResults: in.TestResults, ReviewFocus: in.ReviewFocus, IncludePaths: in.IncludePaths, ExcludePaths: in.ExcludePaths, UntrackedFiles: untracked, ExcludedFiles: excluded, RedactionCount: redactions})
	result, err := s.Claude.Run(ctx, claude.Request{RepositoryPath: root, Prompt: prompt, SystemPrompt: SystemPrompt, Schema: ResponseSchema, Model: in.Model, FallbackModel: in.FallbackModel, Effort: in.Effort, MaxTurns: in.MaxTurns, Timeout: time.Duration(in.TimeoutSeconds) * time.Second})
	if err != nil {
		return ReviewOutput{}, s.persistReviewFailure(ctx, "review_diff", &record, result.SessionID, err)
	}
	response, err := ParseResponse(result.StructuredOutput)
	if err != nil {
		return ReviewOutput{}, s.persistReviewFailure(ctx, "review_diff", &record, result.SessionID, apperr.Wrap("invalid_claude_output", "Claude returned an invalid structured response.", err, map[string]any{"stage": "response_schema_validation"}))
	}
	record.ClaudeSessionID = result.SessionID
	record.Status = session.ReviewStatusOpen
	record.UpdatedAt = s.Now().UTC()
	if err := s.updateDetached(ctx, record); err != nil {
		return ReviewOutput{}, s.completedPersistenceFailure("review_diff", record, false, err)
	}
	s.log("review_diff", reviewID, started, len(diff), len(response.Findings))
	return ReviewOutput{ReviewID: reviewID, ClaudeSessionID: result.SessionID, Response: response, ExcludedFiles: excluded, RedactionCount: redactions}, nil
}

func (s *Service) ContinueReview(ctx context.Context, in ContinueReviewInput) (ReviewOutput, error) {
	started := time.Now()
	if in.ReviewID == "" || in.Message == "" {
		return ReviewOutput{}, apperr.New("invalid_request", "review_id and message are required.", nil)
	}
	unlock, ok := s.locks.try(in.ReviewID)
	if !ok {
		return ReviewOutput{}, apperr.New("review_busy", "This review is already in use.", nil)
	}
	defer unlock()
	record, err := s.Store.Get(ctx, in.ReviewID)
	if err != nil {
		return ReviewOutput{}, mapError(err)
	}
	if record.Status == session.ReviewStatusClosed {
		return ReviewOutput{}, apperr.New("review_closed", "This review is closed.", nil)
	}
	if record.ClaudeSessionID == "" {
		return ReviewOutput{}, apperr.New("review_not_resumable", "Claude did not provide a session_id before this review stopped; start a new review.", map[string]any{"review_id": record.ReviewID, "status": record.Status})
	}
	if in.RepositoryPath != "" {
		root, rootErr := s.Git.Root(ctx, in.RepositoryPath)
		if rootErr != nil {
			return ReviewOutput{}, mapError(rootErr)
		}
		if !samePath(root, record.RepositoryPath) {
			return ReviewOutput{}, apperr.New("repository_mismatch", "The requested repository does not match the review repository.", map[string]any{"expected": record.RepositoryPath, "actual": root})
		}
	}
	var diff string
	var untracked, excluded []string
	var redactions int
	if in.RefreshDiff {
		diff, untracked, excluded, redactions, err = s.prepareDiff(ctx, record.RepositoryPath, record.BaseRef, record.IncludePaths, record.ExcludePaths)
		if err != nil {
			return ReviewOutput{}, err
		}
	}
	prompt := ContinuePrompt(in.Message, diff, in.TestResults, record.IncludePaths, record.ExcludePaths, untracked, excluded, redactions, in.RefreshDiff)
	if record.FallbackModel == "" {
		record.FallbackModel = s.DefaultFallbackModel
	}
	if record.Effort == "" {
		record.Effort = s.DefaultEffort
	}
	if in.TimeoutSeconds == 0 {
		in.TimeoutSeconds = record.TimeoutSeconds
	}
	if err := s.resolveTimeout(&in.TimeoutSeconds); err != nil {
		return ReviewOutput{}, err
	}
	result, err := s.Claude.Run(ctx, claude.Request{RepositoryPath: record.RepositoryPath, Prompt: prompt, Schema: ResponseSchema, Model: record.Model, FallbackModel: record.FallbackModel, Effort: record.Effort, MaxTurns: record.MaxTurns, SessionID: record.ClaudeSessionID, Timeout: time.Duration(in.TimeoutSeconds) * time.Second})
	if err != nil {
		return ReviewOutput{}, s.persistReviewFailure(ctx, "continue_review", &record, result.SessionID, err)
	}
	response, err := ParseResponse(result.StructuredOutput)
	if err != nil {
		return ReviewOutput{}, s.persistReviewFailure(ctx, "continue_review", &record, result.SessionID, apperr.Wrap("invalid_claude_output", "Claude returned an invalid structured response.", err, map[string]any{"stage": "response_schema_validation"}))
	}
	record.UpdatedAt = s.Now().UTC()
	record.Status = session.ReviewStatusOpen
	record.LastErrorCode = ""
	record.LastErrorAt = nil
	if err := s.updateDetached(ctx, record); err != nil {
		return ReviewOutput{}, s.completedPersistenceFailure("continue_review", record, true, err)
	}
	s.log("continue_review", record.ReviewID, started, len(diff), len(response.Findings))
	return ReviewOutput{ReviewID: record.ReviewID, ClaudeSessionID: record.ClaudeSessionID, Response: response, ExcludedFiles: excluded, RedactionCount: redactions}, nil
}

func (s *Service) GetReview(ctx context.Context, id string) (session.ReviewSession, error) {
	r, err := s.Store.Get(ctx, id)
	if err != nil {
		return r, mapError(err)
	}
	return r, nil
}

func (s *Service) ListReviews(ctx context.Context, in ListReviewsInput) ([]session.ReviewSession, error) {
	if in.Status != "" && !validReviewStatus(in.Status) {
		return nil, apperr.New("invalid_request", "The status filter must be pending, open, interrupted, failed, or closed.", nil)
	}
	var root string
	if in.RepositoryPath != "" {
		var err error
		root, err = s.Git.Root(ctx, in.RepositoryPath)
		if err != nil {
			return nil, mapError(err)
		}
	}
	all, err := s.Store.List(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]session.ReviewSession, 0, len(all))
	for _, r := range all {
		if root != "" && !samePath(root, r.RepositoryPath) {
			continue
		}
		if in.Status != "" && in.Status != r.Status {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

func (s *Service) CloseReview(ctx context.Context, in CloseReviewInput) (CloseOutput, error) {
	unlock, ok := s.locks.try(in.ReviewID)
	if !ok {
		return CloseOutput{}, apperr.New("review_busy", "This review is already in use.", nil)
	}
	defer unlock()
	r, err := s.Store.Get(ctx, in.ReviewID)
	if err != nil {
		return CloseOutput{}, mapError(err)
	}
	if in.DeleteClaudeSession {
		if err := s.Store.Delete(ctx, in.ReviewID); err != nil {
			return CloseOutput{}, mapError(err)
		}
		return CloseOutput{ReviewID: in.ReviewID, Status: "deleted", LocalAssociationDeleted: true}, nil
	}
	r.Status, r.UpdatedAt = session.ReviewStatusClosed, s.Now().UTC()
	if err := s.Store.Update(ctx, r); err != nil {
		return CloseOutput{}, mapError(err)
	}
	return CloseOutput{ReviewID: in.ReviewID, Status: string(r.Status)}, nil
}

func (s *Service) prepareDiff(ctx context.Context, root, base string, include, exclude []string) (string, []string, []string, int, error) {
	scope := gitservice.PathScope{Include: include, Exclude: exclude}
	diff, err := s.Git.Diff(ctx, root, base, scope)
	if err != nil {
		return "", nil, nil, 0, mapError(err)
	}
	untracked, err := s.Git.UntrackedFiles(ctx, root, scope)
	if err != nil {
		return "", nil, nil, 0, mapError(err)
	}
	safeUntracked, excludedUntracked := security.FilterUntracked(untracked)
	clean, err := security.SanitizeDiff(diff)
	if err != nil {
		return "", nil, nil, 0, mapError(err)
	}
	excluded := append(clean.ExcludedFiles, excludedUntracked...)
	return clean.Content, safeUntracked, excluded, clean.Redactions, nil
}

func mapError(err error) error {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		return ae
	}
	switch {
	case errors.Is(err, session.ErrNotFound):
		return apperr.New("review_not_found", "No review matches this identifier.", nil)
	case errors.Is(err, gitservice.ErrInvalidRepository):
		return apperr.Wrap("invalid_repository", "The path does not point to a valid Git repository.", err, nil)
	case errors.Is(err, gitservice.ErrInvalidBaseRef):
		return apperr.Wrap("invalid_base_ref", "The Git base reference is invalid.", err, nil)
	case errors.Is(err, gitservice.ErrInvalidPathScope):
		return apperr.Wrap("invalid_path_scope", "include_paths and exclude_paths must contain safe repository-relative paths.", err, nil)
	case errors.Is(err, gitservice.ErrDiffTooLarge):
		return apperr.Wrap("diff_too_large", "The diff exceeds the configured limit; reduce the scope of the change.", err, nil)
	case errors.Is(err, security.ErrPrivateKey):
		return apperr.New("sensitive_content_detected", "A complete private key was detected; the review was rejected.", nil)
	case errors.Is(err, claude.ErrMaxTurns):
		return apperr.Wrap("claude_max_turns", "Claude reached max_turns before producing a structured review; increase max_turns or narrow the review scope.", err, claudeFailureDetails(err, claude.StageProcessExit))
	case errors.Is(err, claude.ErrTimeout):
		return apperr.Wrap("claude_timeout", "Claude did not respond before a timeout; narrow the review scope or ensure the MCP client deadline exceeds timeout_seconds.", err, claudeFailureDetails(err, claude.StageProcessExit))
	case errors.Is(err, claude.ErrCanceled):
		return apperr.Wrap("claude_canceled", "The enclosing MCP request was canceled before Claude completed; resume the review when a Claude session ID was captured.", err, claudeFailureDetails(err, claude.StageProcessExit))
	case errors.Is(err, claude.ErrOutputTooLarge):
		return apperr.Wrap("claude_output_too_large", "Claude output exceeds the configured limit.", err, claudeFailureDetails(err, claude.StageStreamParsing))
	case errors.Is(err, claude.ErrSessionIDMissing):
		return apperr.Wrap("claude_session_id_missing", "Claude returned no session_id.", err, claudeFailureDetails(err, claude.StageMissingSessionID))
	case errors.Is(err, claude.ErrInvalidOutput):
		return apperr.Wrap("invalid_claude_output", "Claude output is invalid.", err, claudeFailureDetails(err, claude.StageStreamParsing))
	case errors.Is(err, claude.ErrProcess):
		return apperr.Wrap("claude_failed", "The Claude process exited unsuccessfully.", err, claudeFailureDetails(err, claude.StageProcessExit))
	case errors.Is(err, claude.ErrNotFound):
		return apperr.New("claude_not_found", "The Claude Code binary was not found.", map[string]any{"stage": claude.StageProcessStart})
	case errors.Is(err, claude.ErrNotAuthenticated):
		return apperr.Wrap("claude_not_authenticated", "Claude Code is not authenticated; run claude auth login.", err, claudeFailureDetails(err, claude.StageAuthentication))
	default:
		return apperr.Wrap("storage_error", "A local operation failed.", err, nil)
	}
}

func claudeFailureDetails(err error, fallbackStage string) map[string]any {
	var runErr *claude.RunError
	if errors.As(err, &runErr) {
		return runErr.PublicDetails()
	}
	return map[string]any{"stage": fallbackStage}
}

func (s *Service) reviewFailure(tool, correlationID string, err error) error {
	mapped := mapError(err)
	var appErr *apperr.Error
	if !errors.As(mapped, &appErr) {
		return mapped
	}
	details := make(map[string]any, len(appErr.Details)+1)
	for key, value := range appErr.Details {
		details[key] = value
	}
	details["correlation_id"] = correlationID
	appErr.Details = details
	if s.Logger != nil {
		s.Logger.Error("review failed", "tool", tool, "correlation_id", correlationID, "error_code", appErr.Code, "details", details)
	}
	return appErr
}

func (s *Service) persistReviewFailure(ctx context.Context, tool string, record *session.ReviewSession, returnedSessionID string, err error) error {
	mapped := s.reviewFailure(tool, record.ReviewID, err)
	var appErr *apperr.Error
	if !errors.As(mapped, &appErr) {
		return mapped
	}
	if record.ClaudeSessionID == "" {
		record.ClaudeSessionID = returnedSessionID
	}
	now := s.Now().UTC()
	record.UpdatedAt = now
	record.LastErrorCode = appErr.Code
	record.LastErrorAt = &now
	if record.ClaudeSessionID == "" {
		record.Status = session.ReviewStatusFailed
	} else {
		record.Status = session.ReviewStatusInterrupted
	}
	details := make(map[string]any, len(appErr.Details)+4)
	for key, value := range appErr.Details {
		details[key] = value
	}
	details["review_id"] = record.ReviewID
	details["resumable"] = record.ClaudeSessionID != ""
	if record.ClaudeSessionID != "" {
		details["claude_session_id"] = record.ClaudeSessionID
	}
	if persistErr := s.updateDetached(ctx, *record); persistErr != nil {
		details["persistence_error"] = "failed to update the local review record"
		if s.Logger != nil {
			s.Logger.Error("failed to persist interrupted review", "review_id", record.ReviewID, "error", persistErr)
		}
	}
	appErr.Details = details
	return appErr
}

func (s *Service) updateDetached(ctx context.Context, record session.ReviewSession) error {
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	return s.Store.Update(persistCtx, record)
}

func (s *Service) completedPersistenceFailure(tool string, record session.ReviewSession, resumable bool, err error) error {
	details := map[string]any{
		"stage":             "session_persistence",
		"review_id":         record.ReviewID,
		"claude_session_id": record.ClaudeSessionID,
		"resumable":         resumable,
	}
	if s.Logger != nil {
		s.Logger.Error("completed review persistence failed", "tool", tool, "review_id", record.ReviewID, "claude_session_id", record.ClaudeSessionID, "error", err)
	}
	return apperr.Wrap("storage_error", "Claude completed, but the local review record could not be updated.", err, details)
}

func (s *Service) resolveTimeout(seconds *int) error {
	if *seconds == 0 {
		*seconds = s.DefaultTimeoutSeconds
	}
	if *seconds == 0 {
		*seconds = 600
	}
	if *seconds < 1 || *seconds > 1200 {
		return apperr.New("invalid_request", "timeout_seconds must be between 1 and 1200.", map[string]any{"timeout_seconds": *seconds})
	}
	return nil
}

func validReviewStatus(status session.ReviewStatus) bool {
	switch status {
	case session.ReviewStatusPending, session.ReviewStatusOpen, session.ReviewStatusInterrupted, session.ReviewStatusFailed, session.ReviewStatusClosed:
		return true
	default:
		return false
	}
}

func (s *Service) log(tool, reviewID string, start time.Time, diffBytes, findings int) {
	if s.Logger != nil {
		s.Logger.Info("review completed", "tool", tool, "review_id", reviewID, "duration_ms", time.Since(start).Milliseconds(), "diff_bytes", diffBytes, "findings", findings)
	}
}

func samePath(a, b string) bool {
	aa, _ := filepath.Abs(a)
	bb, _ := filepath.Abs(b)
	return filepath.Clean(aa) == filepath.Clean(bb)
}

type sessionLocks struct {
	mu     sync.Mutex
	values map[string]bool
}

func (l *sessionLocks) try(id string) (func(), bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.values[id] {
		return nil, false
	}
	l.values[id] = true
	return func() {
		l.mu.Lock()
		delete(l.values, id)
		l.mu.Unlock()
	}, true
}

func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	s := hex.EncodeToString(b[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", s[0:8], s[8:12], s[12:16], s[16:20], s[20:]), nil
}
