package config

import (
	"strings"
	"testing"
)

func TestDefaultsUseStrongestReviewStrategy(t *testing.T) {
	cfg, err := Defaults()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultModel != "fable" || cfg.DefaultFallbackModel != "opus" || cfg.DefaultEffort != "max" {
		t.Fatalf("unexpected model strategy: primary=%q fallback=%q effort=%q", cfg.DefaultModel, cfg.DefaultFallbackModel, cfg.DefaultEffort)
	}
	if cfg.TimeoutSeconds != 240 {
		t.Fatalf("unexpected interactive timeout: %d", cfg.TimeoutSeconds)
	}
	if cfg.AsyncTimeoutSeconds != 1200 {
		t.Fatalf("unexpected asynchronous timeout: %d", cfg.AsyncTimeoutSeconds)
	}
}

func TestValidateReportsSpecificTimeoutField(t *testing.T) {
	cfg, err := Defaults()
	if err != nil {
		t.Fatal(err)
	}
	cfg.AsyncTimeoutSeconds = 1201
	err = validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "async_timeout_seconds") || !strings.Contains(err.Error(), "1201") {
		t.Fatalf("err=%v", err)
	}
}

func TestValidEffort(t *testing.T) {
	for _, value := range []string{"low", "medium", "high", "xhigh", "max"} {
		if !ValidEffort(value) {
			t.Errorf("valid effort rejected: %s", value)
		}
	}
	for _, value := range []string{"", "maximum", "ultra"} {
		if ValidEffort(value) {
			t.Errorf("invalid effort accepted: %s", value)
		}
	}
}
