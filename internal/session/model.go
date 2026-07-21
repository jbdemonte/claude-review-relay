package session

import "time"

type ReviewStatus string

const (
	ReviewStatusPending     ReviewStatus = "pending"
	ReviewStatusOpen        ReviewStatus = "open"
	ReviewStatusInterrupted ReviewStatus = "interrupted"
	ReviewStatusFailed      ReviewStatus = "failed"
	ReviewStatusClosed      ReviewStatus = "closed"
)

type ReviewSession struct {
	ReviewID        string       `json:"review_id"`
	ClaudeSessionID string       `json:"claude_session_id"`
	RepositoryPath  string       `json:"repository_path"`
	Goal            string       `json:"goal"`
	BaseRef         string       `json:"base_ref"`
	IncludePaths    []string     `json:"include_paths,omitempty"`
	ExcludePaths    []string     `json:"exclude_paths,omitempty"`
	HeadSHAAtStart  string       `json:"head_sha_at_start"`
	Model           string       `json:"model"`
	FallbackModel   string       `json:"fallback_model,omitempty"`
	Effort          string       `json:"effort,omitempty"`
	MaxTurns        int          `json:"max_turns"`
	TimeoutSeconds  int          `json:"timeout_seconds,omitempty"`
	Status          ReviewStatus `json:"status"`
	LastErrorCode   string       `json:"last_error_code,omitempty"`
	LastErrorAt     *time.Time   `json:"last_error_at,omitempty"`
	CreatedAt       time.Time    `json:"created_at"`
	UpdatedAt       time.Time    `json:"updated_at"`
}
