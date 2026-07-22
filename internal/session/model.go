package session

import (
	"encoding/json"
	"time"
)

type ReviewStatus string

const (
	ReviewStatusPending     ReviewStatus = "pending"
	ReviewStatusOpen        ReviewStatus = "open"
	ReviewStatusWaiting     ReviewStatus = "waiting_for_quota"
	ReviewStatusInterrupted ReviewStatus = "interrupted"
	ReviewStatusFailed      ReviewStatus = "failed"
	ReviewStatusClosed      ReviewStatus = "closed"
)

type ReviewSession struct {
	ReviewID           string          `json:"review_id"`
	ClaudeSessionID    string          `json:"claude_session_id"`
	RepositoryPath     string          `json:"repository_path"`
	Goal               string          `json:"goal"`
	BaseRef            string          `json:"base_ref"`
	BaseSHAAtStart     string          `json:"base_sha_at_start,omitempty"`
	IncludePaths       []string        `json:"include_paths,omitempty"`
	ExcludePaths       []string        `json:"exclude_paths,omitempty"`
	HeadSHAAtStart     string          `json:"head_sha_at_start"`
	Model              string          `json:"model"`
	FallbackModel      string          `json:"fallback_model,omitempty"`
	Effort             string          `json:"effort,omitempty"`
	MaxTurns           int             `json:"max_turns"`
	TimeoutSeconds     int             `json:"timeout_seconds,omitempty"`
	Status             ReviewStatus    `json:"status"`
	ActiveOperation    string          `json:"active_operation,omitempty"`
	ResponseSequence   int             `json:"response_sequence,omitempty"`
	LastResponse       json.RawMessage `json:"last_response,omitempty"`
	LastExcludedFiles  []string        `json:"last_excluded_files,omitempty"`
	LastRedactionCount int             `json:"last_redaction_count,omitempty"`
	LastErrorCode      string          `json:"last_error_code,omitempty"`
	LastErrorDetails   map[string]any  `json:"last_error_details,omitempty"`
	LastErrorAt        *time.Time      `json:"last_error_at,omitempty"`
	RetryAt            *time.Time      `json:"retry_at,omitempty"`
	RetryOperation     string          `json:"retry_operation,omitempty"`
	ReviewFocus        []string        `json:"review_focus,omitempty"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
}
