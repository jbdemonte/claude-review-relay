package main

import "testing"

func TestParseDoctorOptions(t *testing.T) {
	if smoke, err := parseDoctorOptions(nil); err != nil || smoke {
		t.Fatalf("default: smoke=%v err=%v", smoke, err)
	}
	if smoke, err := parseDoctorOptions([]string{"--review-smoke-test"}); err != nil || !smoke {
		t.Fatalf("smoke: smoke=%v err=%v", smoke, err)
	}
	for _, args := range [][]string{{"--typo"}, {"--review-smoke-test", "extra"}} {
		if _, err := parseDoctorOptions(args); err == nil {
			t.Fatalf("accepted invalid options: %v", args)
		}
	}
}
