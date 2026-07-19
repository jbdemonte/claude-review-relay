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
	gitservice "github.com/jbd/claude-reviewer/internal/git"
	"github.com/jbd/claude-reviewer/internal/security"
	"github.com/jbd/claude-reviewer/internal/session"
)

type Service struct {
	Store                session.SessionStore
	Git                  gitservice.GitService
	Claude               claude.Client
	DefaultModel         string
	DefaultFallbackModel string
	DefaultEffort        string
	DefaultMaxTurns      int
	Now                  func() time.Time
	Logger               *slog.Logger
	locks                sessionLocks
}

type ReviewDiffInput struct {
	RepositoryPath    string   `json:"repository_path" jsonschema:"absolute path of the Git repository"`
	Goal              string   `json:"goal" jsonschema:"functional goal of the implemented change"`
	BaseRef           string   `json:"base_ref,omitempty" jsonschema:"Git base reference, defaults to HEAD"`
	ReviewFocus       []string `json:"review_focus,omitempty"`
	AdditionalContext string   `json:"additional_context,omitempty"`
	TestResults       string   `json:"test_results,omitempty"`
	Model             string   `json:"model,omitempty"`
	FallbackModel     string   `json:"fallback_model,omitempty"`
	Effort            string   `json:"effort,omitempty"`
	MaxTurns          int      `json:"max_turns,omitempty"`
}

type ContinueReviewInput struct {
	ReviewID       string `json:"review_id"`
	Message        string `json:"message"`
	RefreshDiff    bool   `json:"refresh_diff,omitempty"`
	TestResults    string `json:"test_results,omitempty"`
	RepositoryPath string `json:"repository_path,omitempty"`
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

func NewService(store session.SessionStore, git gitservice.GitService, client claude.Client, model, fallbackModel, effort string, maxTurns int, logger *slog.Logger) *Service {
	return &Service{Store: store, Git: git, Claude: client, DefaultModel: model, DefaultFallbackModel: fallbackModel, DefaultEffort: effort, DefaultMaxTurns: maxTurns, Logger: logger, Now: time.Now, locks: sessionLocks{values: map[string]chan struct{}{}}}
}

func (s *Service) ReviewDiff(ctx context.Context, in ReviewDiffInput) (ReviewOutput, error) {
	started := time.Now()
	if in.RepositoryPath == "" || in.Goal == "" {
		return ReviewOutput{}, apperr.New("invalid_request", "repository_path et goal sont obligatoires.", nil)
	}
	reviewID, err := newUUID()
	if err != nil {
		return ReviewOutput{}, apperr.Wrap("storage_error", "Impossible de générer l’identifiant de revue.", err, nil)
	}
	root, err := s.Git.Root(ctx, in.RepositoryPath)
	if err != nil {
		return ReviewOutput{}, mapError(err)
	}
	if in.BaseRef == "" {
		in.BaseRef = "HEAD"
	}
	diff, untracked, excluded, redactions, err := s.prepareDiff(ctx, root, in.BaseRef)
	if err != nil {
		return ReviewOutput{}, err
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
	if in.MaxTurns <= 0 {
		in.MaxTurns = s.DefaultMaxTurns
	}
	prompt := InitialPrompt(InitialPromptInput{Goal: in.Goal, BaseRef: in.BaseRef, Diff: diff, AdditionalContext: in.AdditionalContext, TestResults: in.TestResults, ReviewFocus: in.ReviewFocus, UntrackedFiles: untracked, ExcludedFiles: excluded, RedactionCount: redactions})
	result, err := s.Claude.Run(ctx, claude.Request{RepositoryPath: root, Prompt: prompt, SystemPrompt: SystemPrompt, Schema: ResponseSchema, Model: in.Model, FallbackModel: in.FallbackModel, Effort: in.Effort, MaxTurns: in.MaxTurns})
	if err != nil {
		return ReviewOutput{}, mapError(err)
	}
	response, err := ParseResponse(result.StructuredOutput)
	if err != nil {
		return ReviewOutput{}, apperr.Wrap("invalid_claude_output", "La réponse structurée de Claude est invalide.", err, nil)
	}
	now := s.Now().UTC()
	record := session.ReviewSession{ReviewID: reviewID, ClaudeSessionID: result.SessionID, RepositoryPath: root, Goal: in.Goal, BaseRef: in.BaseRef, HeadSHAAtStart: head, Model: in.Model, FallbackModel: in.FallbackModel, Effort: in.Effort, MaxTurns: in.MaxTurns, Status: session.ReviewStatusOpen, CreatedAt: now, UpdatedAt: now}
	if err := s.Store.Create(ctx, record); err != nil {
		return ReviewOutput{}, apperr.Wrap("storage_error", "Impossible de persister la session de revue.", err, nil)
	}
	s.log("review_diff", reviewID, started, len(diff), len(response.Findings))
	return ReviewOutput{ReviewID: reviewID, ClaudeSessionID: result.SessionID, Response: response, ExcludedFiles: excluded, RedactionCount: redactions}, nil
}

func (s *Service) ContinueReview(ctx context.Context, in ContinueReviewInput) (ReviewOutput, error) {
	started := time.Now()
	if in.ReviewID == "" || in.Message == "" {
		return ReviewOutput{}, apperr.New("invalid_request", "review_id et message sont obligatoires.", nil)
	}
	unlock, ok := s.locks.try(in.ReviewID)
	if !ok {
		return ReviewOutput{}, apperr.New("review_busy", "Cette revue est déjà en cours d’utilisation.", nil)
	}
	defer unlock()
	record, err := s.Store.Get(ctx, in.ReviewID)
	if err != nil {
		return ReviewOutput{}, mapError(err)
	}
	if record.Status == session.ReviewStatusClosed {
		return ReviewOutput{}, apperr.New("review_closed", "Cette revue est fermée.", nil)
	}
	if in.RepositoryPath != "" {
		root, rootErr := s.Git.Root(ctx, in.RepositoryPath)
		if rootErr != nil {
			return ReviewOutput{}, mapError(rootErr)
		}
		if !samePath(root, record.RepositoryPath) {
			return ReviewOutput{}, apperr.New("repository_mismatch", "Le dépôt demandé ne correspond pas à celui de la revue.", map[string]any{"expected": record.RepositoryPath, "actual": root})
		}
	}
	var diff string
	var untracked, excluded []string
	var redactions int
	if in.RefreshDiff {
		diff, untracked, excluded, redactions, err = s.prepareDiff(ctx, record.RepositoryPath, record.BaseRef)
		if err != nil {
			return ReviewOutput{}, err
		}
	}
	prompt := ContinuePrompt(in.Message, diff, in.TestResults, untracked, excluded, redactions)
	if record.FallbackModel == "" {
		record.FallbackModel = s.DefaultFallbackModel
	}
	if record.Effort == "" {
		record.Effort = s.DefaultEffort
	}
	result, err := s.Claude.Run(ctx, claude.Request{RepositoryPath: record.RepositoryPath, Prompt: prompt, Schema: ResponseSchema, Model: record.Model, FallbackModel: record.FallbackModel, Effort: record.Effort, MaxTurns: record.MaxTurns, SessionID: record.ClaudeSessionID})
	if err != nil {
		return ReviewOutput{}, mapError(err)
	}
	response, err := ParseResponse(result.StructuredOutput)
	if err != nil {
		return ReviewOutput{}, apperr.Wrap("invalid_claude_output", "La réponse structurée de Claude est invalide.", err, nil)
	}
	record.UpdatedAt = s.Now().UTC()
	if err := s.Store.Update(ctx, record); err != nil {
		return ReviewOutput{}, apperr.Wrap("storage_error", "Impossible de mettre à jour la session de revue.", err, nil)
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
	if in.Status != "" && in.Status != session.ReviewStatusOpen && in.Status != session.ReviewStatusClosed {
		return nil, apperr.New("invalid_request", "Le filtre status doit valoir open ou closed.", nil)
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
		return CloseOutput{}, apperr.New("review_busy", "Cette revue est déjà en cours d’utilisation.", nil)
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

func (s *Service) prepareDiff(ctx context.Context, root, base string) (string, []string, []string, int, error) {
	diff, err := s.Git.Diff(ctx, root, base)
	if err != nil {
		return "", nil, nil, 0, mapError(err)
	}
	untracked, err := s.Git.UntrackedFiles(ctx, root)
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
		return apperr.New("review_not_found", "Aucune revue ne correspond à cet identifiant.", nil)
	case errors.Is(err, gitservice.ErrInvalidRepository):
		return apperr.Wrap("invalid_repository", "Le chemin ne désigne pas un dépôt Git valide.", err, nil)
	case errors.Is(err, gitservice.ErrInvalidBaseRef):
		return apperr.Wrap("invalid_base_ref", "La référence Git de base est invalide.", err, nil)
	case errors.Is(err, gitservice.ErrDiffTooLarge):
		return apperr.Wrap("diff_too_large", "Le diff dépasse la limite configurée ; réduisez la portée du changement.", err, nil)
	case errors.Is(err, security.ErrPrivateKey):
		return apperr.New("sensitive_content_detected", "Une clé privée complète a été détectée ; la revue est refusée.", nil)
	case errors.Is(err, claude.ErrTimeout):
		return apperr.New("claude_timeout", "Claude n’a pas répondu avant le délai maximal.", nil)
	case errors.Is(err, claude.ErrSessionIDMissing):
		return apperr.New("claude_session_id_missing", "Claude n’a retourné aucun session_id.", nil)
	case errors.Is(err, claude.ErrInvalidOutput):
		return apperr.Wrap("invalid_claude_output", "La sortie de Claude est invalide.", err, nil)
	case errors.Is(err, claude.ErrProcess):
		return apperr.Wrap("claude_failed", "Le processus Claude a échoué.", err, nil)
	case errors.Is(err, claude.ErrNotFound):
		return apperr.New("claude_not_found", "Le binaire Claude Code est introuvable.", nil)
	case errors.Is(err, claude.ErrNotAuthenticated):
		return apperr.New("claude_not_authenticated", "Claude Code n’est pas authentifié ; exécutez claude auth login.", nil)
	default:
		return apperr.Wrap("storage_error", "Une opération locale a échoué.", err, nil)
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
	values map[string]chan struct{}
}

func (l *sessionLocks) try(id string) (func(), bool) {
	l.mu.Lock()
	ch := l.values[id]
	if ch == nil {
		ch = make(chan struct{}, 1)
		l.values[id] = ch
	}
	l.mu.Unlock()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, true
	default:
		return nil, false
	}
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
