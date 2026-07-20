package security

import (
	"errors"
	"strings"
	"testing"
)

func TestSanitizeDiffExcludesSensitiveFilesAndRedactsTokens(t *testing.T) {
	diff := "diff --git a/.env b/.env\n+SECRET=oops\n" +
		"diff --git a/main.go b/main.go\n+Authorization: Bearer abcdefghijklmnopqrstuvwxyz\n+API_TOKEN=abcdefghijabcdefghij\n"
	r, err := SanitizeDiff(diff)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.ExcludedFiles) != 1 || r.ExcludedFiles[0] != ".env" {
		t.Fatalf("excluded=%v", r.ExcludedFiles)
	}
	if strings.Contains(r.Content, "abcdefghijklmnopqrstuvwxyz") || strings.Contains(r.Content, "abcdefghijabcdefghij") {
		t.Fatal("secret was not redacted")
	}
	if r.Redactions != 2 {
		t.Fatalf("redactions=%d", r.Redactions)
	}
}

func TestSanitizeDiffRejectsPrivateKey(t *testing.T) {
	// Split the marker in source so this repository can review its own diff while
	// the runtime value still exercises a complete PEM block.
	pem := "-----BEGIN " + "PRIVATE KEY-----\nabc\n-----END " + "PRIVATE KEY-----"
	_, err := SanitizeDiff(pem)
	if !errors.Is(err, ErrPrivateKey) {
		t.Fatalf("err=%v", err)
	}
}

func TestRedactTextNeverExposesDiagnosticSecrets(t *testing.T) {
	pem := "-----BEGIN " + "PRIVATE KEY-----\nabc\n-----END " + "PRIVATE KEY-----"
	input := "API_TOKEN=secret-value\nAuthorization: Bearer abcdefghijklmnopqrstuvwxyz\n" + pem
	clean, count := RedactText(input)
	if count != 3 {
		t.Fatalf("redactions=%d content=%q", count, clean)
	}
	for _, secret := range []string{"secret-value", "abcdefghijklmnopqrstuvwxyz", "BEGIN PRIVATE KEY", "abc"} {
		if strings.Contains(clean, secret) {
			t.Fatalf("secret %q leaked in %q", secret, clean)
		}
	}
}

func TestRedactDiagnosticHandlesEmbeddedAndJSONAssignments(t *testing.T) {
	clean := RedactDiagnostic("error password=my pass,word\nnext line\n{\"api_key\":\"second\"}")
	if strings.Contains(clean, "my pass,word") || strings.Contains(clean, "second") {
		t.Fatalf("diagnostic secret leaked in %q", clean)
	}
	if !strings.Contains(clean, "next line") {
		t.Fatalf("redaction crossed a line boundary in %q", clean)
	}
}

func TestSensitiveFilename(t *testing.T) {
	for _, f := range []string{".env.local", "prod-credentials.json", "id_rsa.key", "refresh_token.txt"} {
		if !SensitiveFilename(f) {
			t.Errorf("expected sensitive: %s", f)
		}
	}
	if SensitiveFilename("main.go") {
		t.Error("ordinary filename marked sensitive")
	}
}
