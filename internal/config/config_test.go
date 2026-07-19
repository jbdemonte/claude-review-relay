package config

import "testing"

func TestDefaultsUseStrongestReviewStrategy(t *testing.T) {
	cfg, err := Defaults()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultModel != "fable" || cfg.DefaultFallbackModel != "opus" || cfg.DefaultEffort != "max" {
		t.Fatalf("unexpected model strategy: primary=%q fallback=%q effort=%q", cfg.DefaultModel, cfg.DefaultFallbackModel, cfg.DefaultEffort)
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
