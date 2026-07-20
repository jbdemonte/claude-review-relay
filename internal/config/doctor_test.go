package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCheckDetailsJSONIsOptionalAndStructured(t *testing.T) {
	plain, err := json.Marshal(Check{Name: "plain", OK: true, Detail: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plain), `"details"`) {
		t.Fatalf("empty details were serialized: %s", plain)
	}
	detailed, err := json.Marshal(Check{Name: "failed", Detail: "failed", Details: map[string]any{"correlation_id": "R", "exit_code": 1}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(detailed), `"details":{"correlation_id":"R","exit_code":1}`) {
		t.Fatalf("structured details missing: %s", detailed)
	}
}
