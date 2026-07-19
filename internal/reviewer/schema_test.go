package reviewer

import (
	"encoding/json"
	"testing"
)

func TestParseResponse(t *testing.T) {
	raw := json.RawMessage(`{"verdict":"changes_requested","summary":"bug","findings":[{"id":"F001","severity":"high","category":"correctness","file":"a.go","line":3,"problem":"p","impact":"i","recommendation":"r","confidence":0.9}],"missing_tests":[],"previous_findings":[{"id":"F001","status":"still_open","comment":"unchanged"}]}`)
	r, err := ParseResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if r.Findings[0].ID != "F001" {
		t.Fatalf("r=%+v", r)
	}
}

func TestParseResponseRejectsDuplicateIDs(t *testing.T) {
	raw := json.RawMessage(`{"verdict":"changes_requested","summary":"bug","findings":[{"id":"F001","severity":"low","category":"test","file":"a","problem":"p","impact":"i","recommendation":"r","confidence":1},{"id":"F001","severity":"low","category":"test","file":"a","problem":"p","impact":"i","recommendation":"r","confidence":1}],"missing_tests":[]}`)
	if _, err := ParseResponse(raw); err == nil {
		t.Fatal("expected error")
	}
}
