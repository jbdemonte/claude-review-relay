package reviewer

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

const ResponseSchema = `{"type":"object","additionalProperties":false,"required":["verdict","summary","findings","missing_tests"],"properties":{"verdict":{"type":"string","enum":["approve","changes_requested","needs_context"]},"summary":{"type":"string"},"findings":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["id","severity","category","file","problem","impact","recommendation","confidence"],"properties":{"id":{"type":"string","pattern":"^F[0-9]{3,}$"},"severity":{"type":"string","enum":["critical","high","medium","low"]},"category":{"type":"string","enum":["correctness","regression","architecture","performance","security","concurrency","maintainability","test"]},"file":{"type":"string"},"line":{"type":["integer","null"]},"problem":{"type":"string"},"impact":{"type":"string"},"recommendation":{"type":"string"},"confidence":{"type":"number","minimum":0,"maximum":1}}}},"missing_tests":{"type":"array","items":{"type":"string"}},"questions":{"type":"array","items":{"type":"string"}},"previous_findings":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["id","status","comment"],"properties":{"id":{"type":"string","pattern":"^F[0-9]{3,}$"},"status":{"type":"string","enum":["resolved","still_open","invalidated","partially_resolved"]},"comment":{"type":"string"}}}}}}`

type ReviewResponse struct {
	Verdict          string            `json:"verdict"`
	Summary          string            `json:"summary"`
	Findings         []Finding         `json:"findings"`
	MissingTests     []string          `json:"missing_tests"`
	Questions        []string          `json:"questions,omitempty"`
	PreviousFindings []PreviousFinding `json:"previous_findings,omitempty"`
}

type Finding struct {
	ID             string  `json:"id"`
	Severity       string  `json:"severity"`
	Category       string  `json:"category"`
	File           string  `json:"file"`
	Line           *int    `json:"line,omitempty"`
	Problem        string  `json:"problem"`
	Impact         string  `json:"impact"`
	Recommendation string  `json:"recommendation"`
	Confidence     float64 `json:"confidence"`
}

type PreviousFinding struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Comment string `json:"comment"`
}

var findingID = regexp.MustCompile(`^F[0-9]{3,}$`)

func ParseResponse(raw json.RawMessage) (ReviewResponse, error) {
	var r ReviewResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return r, fmt.Errorf("decode structured response: %w", err)
	}
	if err := r.Validate(); err != nil {
		return r, err
	}
	return r, nil
}

func (r ReviewResponse) Validate() error {
	if r.Verdict != "approve" && r.Verdict != "changes_requested" && r.Verdict != "needs_context" {
		return errors.New("invalid verdict")
	}
	if r.Summary == "" || r.Findings == nil || r.MissingTests == nil {
		return errors.New("missing required review response fields")
	}
	seen := map[string]bool{}
	validSeverity := map[string]bool{"critical": true, "high": true, "medium": true, "low": true}
	validCategory := map[string]bool{"correctness": true, "regression": true, "architecture": true, "performance": true, "security": true, "concurrency": true, "maintainability": true, "test": true}
	for _, f := range r.Findings {
		if !findingID.MatchString(f.ID) || seen[f.ID] {
			return fmt.Errorf("invalid or duplicate finding id %q", f.ID)
		}
		seen[f.ID] = true
		if !validSeverity[f.Severity] || !validCategory[f.Category] || f.File == "" || f.Problem == "" || f.Impact == "" || f.Recommendation == "" || f.Confidence < 0 || f.Confidence > 1 {
			return fmt.Errorf("invalid finding %q", f.ID)
		}
	}
	validStatus := map[string]bool{"resolved": true, "still_open": true, "invalidated": true, "partially_resolved": true}
	for _, f := range r.PreviousFindings {
		if !findingID.MatchString(f.ID) || !validStatus[f.Status] || f.Comment == "" {
			return fmt.Errorf("invalid previous finding %q", f.ID)
		}
	}
	return nil
}
