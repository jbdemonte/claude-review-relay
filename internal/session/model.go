package session

import "time"

type ReviewStatus string

const (
	ReviewStatusOpen   ReviewStatus = "open"
	ReviewStatusClosed ReviewStatus = "closed"
)

type ReviewSession struct {
	ReviewID        string       `json:"review_id"`
	ClaudeSessionID string       `json:"claude_session_id"`
	RepositoryPath  string       `json:"repository_path"`
	Goal            string       `json:"goal"`
	BaseRef         string       `json:"base_ref"`
	HeadSHAAtStart  string       `json:"head_sha_at_start"`
	Model           string       `json:"model"`
	FallbackModel   string       `json:"fallback_model,omitempty"`
	Effort          string       `json:"effort,omitempty"`
	MaxTurns        int          `json:"max_turns"`
	Status          ReviewStatus `json:"status"`
	CreatedAt       time.Time    `json:"created_at"`
	UpdatedAt       time.Time    `json:"updated_at"`
}
