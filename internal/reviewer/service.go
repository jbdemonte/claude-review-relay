package reviewer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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
	AsyncTimeoutSeconds   int
	WorkerContext         context.Context
	Now                   func() time.Time
	Logger                *slog.Logger
	locks                 sessionLocks
	workerMu              sync.Mutex
	shuttingDown          bool
	workerWG              sync.WaitGroup
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

type AsyncStartOutput struct {
	ReviewID                 string               `json:"review_id"`
	ClaudeSessionID          string               `json:"claude_session_id,omitempty"`
	Status                   session.ReviewStatus `json:"status"`
	Operation                string               `json:"operation"`
	ExpectedResponseSequence int                  `json:"expected_response_sequence"`
	PollAfterSeconds         int                  `json:"poll_after_seconds"`
}

type ReviewStatusOutput struct {
	ReviewID         string               `json:"review_id"`
	ClaudeSessionID  string               `json:"claude_session_id,omitempty"`
	Status           session.ReviewStatus `json:"status"`
	ActiveOperation  string               `json:"active_operation,omitempty"`
	ResponseSequence int                  `json:"response_sequence"`
	Response         *ReviewResponse      `json:"response,omitempty"`
	ExcludedFiles    []string             `json:"excluded_files,omitempty"`
	RedactionCount   int                  `json:"redaction_count"`
	LastErrorCode    string               `json:"last_error_code,omitempty"`
	LastErrorDetails map[string]any       `json:"last_error_details,omitempty"`
	CreatedAt        time.Time            `json:"created_at"`
	UpdatedAt        time.Time            `json:"updated_at"`
}

type preparedReview struct {
	record         session.ReviewSession
	request        claude.Request
	excludedFiles  []string
	redactionCount int
	diffBytes      int
	release        func()
}

func NewService(store session.SessionStore, git gitservice.GitService, client claude.Client, model, fallbackModel, effort string, maxTurns, timeoutSeconds int, logger *slog.Logger) *Service {
	return &Service{Store: store, Git: git, Claude: client, DefaultModel: model, DefaultFallbackModel: fallbackModel, DefaultEffort: effort, DefaultMaxTurns: maxTurns, DefaultTimeoutSeconds: timeoutSeconds, AsyncTimeoutSeconds: 1200, WorkerContext: context.Background(), Logger: logger, Now: time.Now, locks: sessionLocks{values: map[string]bool{}}}
}

func (s *Service) ReviewDiff(ctx context.Context, in ReviewDiffInput) (ReviewOutput, error) {
	prepared, err := s.prepareInitialReview(ctx, in, s.DefaultTimeoutSeconds)
	if err != nil {
		return ReviewOutput{}, err
	}
	defer prepared.release()
	return s.executeReview(ctx, "review_diff", prepared)
}

func (s *Service) StartReview(ctx context.Context, in ReviewDiffInput) (AsyncStartOutput, error) {
	prepared, err := s.prepareInitialReview(ctx, in, s.AsyncTimeoutSeconds)
	if err != nil {
		return AsyncStartOutput{}, err
	}
	out := AsyncStartOutput{ReviewID: prepared.record.ReviewID, Status: session.ReviewStatusPending, Operation: "initial", ExpectedResponseSequence: 1, PollAfterSeconds: 15}
	if err := s.launchReview("start_review", prepared); err != nil {
		return AsyncStartOutput{}, err
	}
	return out, nil
}

func (s *Service) prepareInitialReview(ctx context.Context, in ReviewDiffInput, defaultTimeoutSeconds int) (*preparedReview, error) {
	if in.RepositoryPath == "" || in.Goal == "" {
		return nil, apperr.New("invalid_request", "repository_path and goal are required.", nil)
	}
	reviewID, err := newUUID()
	if err != nil {
		return nil, apperr.Wrap("storage_error", "Failed to generate the review identifier.", err, nil)
	}
	unlock, ok := s.locks.try(reviewID)
	if !ok {
		return nil, apperr.New("review_busy", "This review is already in use.", nil)
	}
	lease, err := session.AcquireReviewLease(s.Store.LeaseDir(), reviewID)
	if err != nil {
		unlock()
		return nil, apperr.Wrap("storage_error", "Failed to acquire the review worker lease.", err, map[string]any{"review_id": reviewID})
	}
	release := func() {
		if err := lease.Release(); err != nil && s.Logger != nil {
			s.Logger.Error("failed to release review lease", "review_id", reviewID, "error", err)
		}
		unlock()
	}
	keepLock := false
	defer func() {
		if !keepLock {
			release()
		}
	}()
	root, err := s.Git.Root(ctx, in.RepositoryPath)
	if err != nil {
		return nil, mapError(err)
	}
	if in.BaseRef == "" {
		in.BaseRef = "HEAD"
	}
	diff, untracked, excluded, redactions, err := s.prepareDiff(ctx, root, in.BaseRef, in.IncludePaths, in.ExcludePaths)
	if err != nil {
		return nil, err
	}
	if diff == "" && len(untracked) == 0 {
		details := map[string]any{"include_paths": in.IncludePaths, "exclude_paths": in.ExcludePaths}
		if len(excluded) > 0 {
			details["sensitive_excluded_files"] = excluded
			return nil, apperr.New("empty_review_scope", "The selected path scope contains only files excluded by the sensitive-content policy.", details)
		}
		return nil, apperr.New("empty_review_scope", "The selected path scope contains no tracked diff or untracked files.", details)
	}
	head, err := s.Git.HeadSHA(ctx, root)
	if err != nil {
		return nil, mapError(err)
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
		return nil, apperr.New("invalid_request", "effort must be low, medium, high, xhigh, or max.", map[string]any{"effort": in.Effort})
	}
	if in.MaxTurns <= 0 {
		in.MaxTurns = s.DefaultMaxTurns
	}
	if err := s.resolveTimeoutWithDefault(&in.TimeoutSeconds, defaultTimeoutSeconds); err != nil {
		return nil, err
	}
	now := s.Now().UTC()
	record := session.ReviewSession{ReviewID: reviewID, RepositoryPath: root, Goal: in.Goal, BaseRef: in.BaseRef, IncludePaths: append([]string(nil), in.IncludePaths...), ExcludePaths: append([]string(nil), in.ExcludePaths...), HeadSHAAtStart: head, Model: in.Model, FallbackModel: in.FallbackModel, Effort: in.Effort, MaxTurns: in.MaxTurns, TimeoutSeconds: in.TimeoutSeconds, Status: session.ReviewStatusPending, ActiveOperation: "initial", LastExcludedFiles: append([]string(nil), excluded...), LastRedactionCount: redactions, CreatedAt: now, UpdatedAt: now}
	if err := s.Store.Create(ctx, record); err != nil {
		return nil, apperr.Wrap("storage_error", "Failed to persist the pending review.", err, nil)
	}
	prompt := InitialPrompt(InitialPromptInput{Goal: in.Goal, BaseRef: in.BaseRef, Diff: diff, AdditionalContext: in.AdditionalContext, TestResults: in.TestResults, ReviewFocus: in.ReviewFocus, IncludePaths: in.IncludePaths, ExcludePaths: in.ExcludePaths, UntrackedFiles: untracked, ExcludedFiles: excluded, RedactionCount: redactions})
	keepLock = true
	return &preparedReview{record: record, request: claude.Request{RepositoryPath: root, Prompt: prompt, SystemPrompt: SystemPrompt, Schema: ResponseSchema, Model: in.Model, FallbackModel: in.FallbackModel, Effort: in.Effort, MaxTurns: in.MaxTurns, Timeout: time.Duration(in.TimeoutSeconds) * time.Second}, excludedFiles: excluded, redactionCount: redactions, diffBytes: len(diff), release: release}, nil
}

func (s *Service) executeReview(ctx context.Context, tool string, prepared *preparedReview) (ReviewOutput, error) {
	started := time.Now()
	result, err := s.Claude.Run(ctx, prepared.request)
	if err != nil {
		return ReviewOutput{}, s.persistReviewFailure(ctx, tool, &prepared.record, result.SessionID, err)
	}
	response, err := ParseResponse(result.StructuredOutput)
	if err != nil {
		return ReviewOutput{}, s.persistReviewFailure(ctx, tool, &prepared.record, result.SessionID, apperr.Wrap("invalid_claude_output", "Claude returned an invalid structured response.", err, map[string]any{"stage": "response_schema_validation"}))
	}
	sessionID := result.SessionID
	if sessionID == "" {
		sessionID = prepared.request.SessionID
	}
	prepared.record.ClaudeSessionID = sessionID
	prepared.record.Status = session.ReviewStatusOpen
	prepared.record.ActiveOperation = ""
	prepared.record.ResponseSequence++
	prepared.record.LastResponse = append(json.RawMessage(nil), result.StructuredOutput...)
	prepared.record.LastErrorCode = ""
	prepared.record.LastErrorDetails = nil
	prepared.record.LastErrorAt = nil
	prepared.record.UpdatedAt = s.Now().UTC()
	if err := s.updateDetached(ctx, prepared.record); err != nil {
		return ReviewOutput{}, s.completedPersistenceFailure(tool, prepared.record, prepared.request.SessionID != "", err)
	}
	s.log(tool, prepared.record.ReviewID, started, prepared.diffBytes, len(response.Findings))
	return ReviewOutput{ReviewID: prepared.record.ReviewID, ClaudeSessionID: sessionID, Response: response, ExcludedFiles: prepared.excludedFiles, RedactionCount: prepared.redactionCount}, nil
}

func (s *Service) launchReview(tool string, prepared *preparedReview) error {
	workerCtx := s.WorkerContext
	if workerCtx == nil {
		workerCtx = context.Background()
	}
	s.workerMu.Lock()
	if s.shuttingDown {
		s.workerMu.Unlock()
		err := apperr.New("server_shutting_down", "The MCP server is shutting down and cannot start another background review.", map[string]any{"review_id": prepared.record.ReviewID})
		persisted := s.persistReviewFailure(workerCtx, tool, &prepared.record, prepared.record.ClaudeSessionID, err)
		prepared.release()
		return persisted
	}
	s.workerWG.Add(1)
	s.workerMu.Unlock()
	go func() {
		defer s.workerWG.Done()
		defer prepared.release()
		defer func() {
			if recover() != nil {
				_ = s.persistReviewFailure(workerCtx, tool, &prepared.record, prepared.record.ClaudeSessionID, apperr.New("worker_failed", "The background review worker stopped unexpectedly.", map[string]any{"stage": "background_worker"}))
			}
		}()
		_, _ = s.executeReview(workerCtx, tool, prepared)
	}()
	return nil
}

func (s *Service) BeginShutdown() {
	s.workerMu.Lock()
	s.shuttingDown = true
	s.workerMu.Unlock()
}

func (s *Service) WaitForWorkers() {
	s.workerWG.Wait()
}

func (s *Service) ContinueReview(ctx context.Context, in ContinueReviewInput) (ReviewOutput, error) {
	prepared, err := s.prepareContinuation(ctx, in, s.DefaultTimeoutSeconds)
	if err != nil {
		return ReviewOutput{}, err
	}
	defer prepared.release()
	return s.executeReview(ctx, "continue_review", prepared)
}

func (s *Service) StartContinueReview(ctx context.Context, in ContinueReviewInput) (AsyncStartOutput, error) {
	if in.TimeoutSeconds == 0 {
		in.TimeoutSeconds = s.AsyncTimeoutSeconds
	}
	prepared, err := s.prepareContinuation(ctx, in, s.AsyncTimeoutSeconds)
	if err != nil {
		return AsyncStartOutput{}, err
	}
	out := AsyncStartOutput{ReviewID: prepared.record.ReviewID, ClaudeSessionID: prepared.record.ClaudeSessionID, Status: session.ReviewStatusPending, Operation: "continuation", ExpectedResponseSequence: prepared.record.ResponseSequence + 1, PollAfterSeconds: 15}
	if err := s.launchReview("start_continue_review", prepared); err != nil {
		return AsyncStartOutput{}, err
	}
	return out, nil
}

func (s *Service) prepareContinuation(ctx context.Context, in ContinueReviewInput, defaultTimeoutSeconds int) (*preparedReview, error) {
	if in.ReviewID == "" || in.Message == "" {
		return nil, apperr.New("invalid_request", "review_id and message are required.", nil)
	}
	unlock, ok := s.locks.try(in.ReviewID)
	if !ok {
		return nil, apperr.New("review_busy", "This review is already in use.", nil)
	}
	lease, err := session.AcquireReviewLease(s.Store.LeaseDir(), in.ReviewID)
	if errors.Is(err, session.ErrLeaseBusy) {
		unlock()
		return nil, apperr.New("review_busy", "This review is already running in another MCP server process.", map[string]any{"review_id": in.ReviewID})
	}
	if err != nil {
		unlock()
		return nil, apperr.Wrap("storage_error", "Failed to acquire the review worker lease.", err, map[string]any{"review_id": in.ReviewID})
	}
	release := func() {
		if err := lease.Release(); err != nil && s.Logger != nil {
			s.Logger.Error("failed to release review lease", "review_id", in.ReviewID, "error", err)
		}
		unlock()
	}
	keepLock := false
	defer func() {
		if !keepLock {
			release()
		}
	}()
	record, err := s.Store.Get(ctx, in.ReviewID)
	if err != nil {
		return nil, mapError(err)
	}
	if record.Status == session.ReviewStatusClosed {
		return nil, apperr.New("review_closed", "This review is closed.", nil)
	}
	if record.ClaudeSessionID == "" {
		return nil, apperr.New("review_not_resumable", "Claude did not provide a session_id before this review stopped; start a new review.", map[string]any{"review_id": record.ReviewID, "status": record.Status})
	}
	if in.RepositoryPath != "" {
		root, rootErr := s.Git.Root(ctx, in.RepositoryPath)
		if rootErr != nil {
			return nil, mapError(rootErr)
		}
		if !samePath(root, record.RepositoryPath) {
			return nil, apperr.New("repository_mismatch", "The requested repository does not match the review repository.", map[string]any{"expected": record.RepositoryPath, "actual": root})
		}
	}
	var diff string
	var untracked, excluded []string
	var redactions int
	if in.RefreshDiff {
		diff, untracked, excluded, redactions, err = s.prepareDiff(ctx, record.RepositoryPath, record.BaseRef, record.IncludePaths, record.ExcludePaths)
		if err != nil {
			return nil, err
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
	if err := s.resolveTimeoutWithDefault(&in.TimeoutSeconds, defaultTimeoutSeconds); err != nil {
		return nil, err
	}
	record.Status = session.ReviewStatusPending
	record.ActiveOperation = "continuation"
	record.UpdatedAt = s.Now().UTC()
	record.LastErrorCode = ""
	record.LastErrorDetails = nil
	record.LastErrorAt = nil
	if in.RefreshDiff {
		record.LastExcludedFiles = append([]string(nil), excluded...)
		record.LastRedactionCount = redactions
	}
	if err := s.updateDetached(ctx, record); err != nil {
		return nil, apperr.Wrap("storage_error", "Failed to mark the review continuation as pending.", err, map[string]any{"review_id": record.ReviewID})
	}
	keepLock = true
	return &preparedReview{record: record, request: claude.Request{RepositoryPath: record.RepositoryPath, Prompt: prompt, Schema: ResponseSchema, Model: record.Model, FallbackModel: record.FallbackModel, Effort: record.Effort, MaxTurns: record.MaxTurns, SessionID: record.ClaudeSessionID, Timeout: time.Duration(in.TimeoutSeconds) * time.Second}, excludedFiles: excluded, redactionCount: redactions, diffBytes: len(diff), release: release}, nil
}

func (s *Service) GetReview(ctx context.Context, id string) (session.ReviewSession, error) {
	r, err := s.Store.Get(ctx, id)
	if err != nil {
		return r, mapError(err)
	}
	return r, nil
}

func (s *Service) GetReviewStatus(ctx context.Context, id string) (ReviewStatusOutput, error) {
	record, err := s.GetReview(ctx, id)
	if err != nil {
		return ReviewStatusOutput{}, err
	}
	if record.Status == session.ReviewStatusPending {
		lease, leaseErr := session.AcquireReviewLease(s.Store.LeaseDir(), record.ReviewID)
		if errors.Is(leaseErr, session.ErrLeaseBusy) {
			return statusOutput(record)
		}
		if leaseErr != nil {
			return ReviewStatusOutput{}, apperr.Wrap("storage_error", "Failed to inspect the review worker lease.", leaseErr, map[string]any{"review_id": record.ReviewID})
		}
		defer func() { _ = lease.Release() }()
		record, err = s.GetReview(ctx, id)
		if err != nil {
			return ReviewStatusOutput{}, err
		}
		if record.Status == session.ReviewStatusPending {
			s.markStoppedWorker(&record)
		}
	}
	return statusOutput(record)
}

func (s *Service) RecoverStaleWorkers(ctx context.Context) error {
	records, err := s.Store.List(ctx)
	if err != nil {
		return mapError(err)
	}
	for _, record := range records {
		if record.Status != session.ReviewStatusPending {
			continue
		}
		lease, leaseErr := session.AcquireReviewLease(s.Store.LeaseDir(), record.ReviewID)
		if errors.Is(leaseErr, session.ErrLeaseBusy) {
			continue
		}
		if leaseErr != nil {
			if s.Logger != nil {
				s.Logger.Error("failed to inspect a background worker lease", "review_id", record.ReviewID, "error", leaseErr)
			}
			continue
		}
		fresh, getErr := s.Store.Get(ctx, record.ReviewID)
		if getErr != nil {
			_ = lease.Release()
			if s.Logger != nil {
				s.Logger.Error("failed to reload a pending review during reconciliation", "review_id", record.ReviewID, "error", getErr)
			}
			continue
		}
		if fresh.Status != session.ReviewStatusPending {
			_ = lease.Release()
			continue
		}
		s.markStoppedWorker(&fresh)
		if err := s.updateDetached(ctx, fresh); err != nil {
			if s.Logger != nil {
				s.Logger.Error("failed to reconcile a stopped background worker", "review_id", fresh.ReviewID, "error", err)
			}
		}
		_ = lease.Release()
	}
	return nil
}

func (s *Service) markStoppedWorker(record *session.ReviewSession) {
	now := s.Now().UTC()
	record.ActiveOperation = ""
	record.UpdatedAt = now
	record.LastErrorCode = "background_worker_stopped"
	record.LastErrorAt = &now
	record.LastErrorDetails = map[string]any{"stage": "background_worker", "review_id": record.ReviewID, "resumable": record.ClaudeSessionID != ""}
	if record.ClaudeSessionID == "" {
		record.Status = session.ReviewStatusFailed
	} else {
		record.Status = session.ReviewStatusInterrupted
	}
}

func statusOutput(record session.ReviewSession) (ReviewStatusOutput, error) {
	output := ReviewStatusOutput{
		ReviewID: record.ReviewID, ClaudeSessionID: record.ClaudeSessionID,
		Status: record.Status, ActiveOperation: record.ActiveOperation,
		ResponseSequence: record.ResponseSequence,
		ExcludedFiles:    append([]string(nil), record.LastExcludedFiles...), RedactionCount: record.LastRedactionCount,
		LastErrorCode: record.LastErrorCode, LastErrorDetails: cloneDetails(record.LastErrorDetails),
		CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}
	if len(record.LastResponse) > 0 {
		response, err := ParseResponse(record.LastResponse)
		if err != nil {
			return ReviewStatusOutput{}, apperr.Wrap("storage_error", "The persisted review response is invalid.", err, map[string]any{"review_id": record.ReviewID})
		}
		output.Response = &response
	}
	return output, nil
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
	lease, leaseErr := session.AcquireReviewLease(s.Store.LeaseDir(), in.ReviewID)
	if errors.Is(leaseErr, session.ErrLeaseBusy) {
		return CloseOutput{}, apperr.New("review_busy", "This review is already running in a background worker.", map[string]any{"review_id": in.ReviewID})
	}
	if leaseErr != nil {
		return CloseOutput{}, apperr.Wrap("storage_error", "Failed to acquire the review worker lease.", leaseErr, map[string]any{"review_id": in.ReviewID})
	}
	defer func() { _ = lease.Release() }()
	r, err := s.Store.Get(ctx, in.ReviewID)
	if err != nil {
		return CloseOutput{}, mapError(err)
	}
	if in.DeleteClaudeSession {
		if err := s.Store.Delete(ctx, in.ReviewID); err != nil {
			return CloseOutput{}, mapError(err)
		}
		if err := session.RemoveReviewLeaseFile(s.Store.LeaseDir(), in.ReviewID); err != nil && s.Logger != nil {
			s.Logger.Warn("failed to remove deleted review lease file", "review_id", in.ReviewID, "error", err)
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
	record.ActiveOperation = ""
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
	appErr.Details = details
	record.LastErrorDetails = cloneDetails(details)
	if persistErr := s.updateDetached(ctx, *record); persistErr != nil {
		details["persistence_error"] = "failed to update the local review record"
		if s.Logger != nil {
			s.Logger.Error("failed to persist interrupted review", "review_id", record.ReviewID, "error", persistErr)
		}
	}
	return appErr
}

func (s *Service) updateDetached(ctx context.Context, record session.ReviewSession) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		lastErr = s.Store.Update(persistCtx, record)
		cancel()
		if lastErr == nil {
			return nil
		}
		if attempt < 2 {
			time.Sleep(50 * time.Millisecond)
		}
	}
	return lastErr
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

func (s *Service) resolveTimeoutWithDefault(seconds *int, defaultSeconds int) error {
	if *seconds == 0 {
		*seconds = defaultSeconds
	}
	if *seconds == 0 {
		*seconds = 600
	}
	if *seconds < 1 || *seconds > 1200 {
		return apperr.New("invalid_request", "timeout_seconds must be between 1 and 1200.", map[string]any{"timeout_seconds": *seconds})
	}
	return nil
}

func cloneDetails(details map[string]any) map[string]any {
	if len(details) == 0 {
		return nil
	}
	cloned := make(map[string]any, len(details))
	for key, value := range details {
		cloned[key] = value
	}
	return cloned
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
