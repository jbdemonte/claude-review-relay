package security

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"
)

var ErrPrivateKey = errors.New("complete private key detected")

type Result struct {
	Content       string
	ExcludedFiles []string
	Redactions    int
}

var (
	privateKeyRE           = regexp.MustCompile(`(?s)-----BEGIN (?:[A-Z0-9 ]+ )?PRIVATE KEY-----.*?-----END (?:[A-Z0-9 ]+ )?PRIVATE KEY-----`)
	assignmentRE           = regexp.MustCompile(`(?im)(^[+ -]?\s*(?:[A-Z0-9_]*(?:TOKEN|SECRET|PASSWORD|API_KEY|PRIVATE_KEY)[A-Z0-9_]*)\s*[=:]\s*)([^\s"']+|"[^"]*"|'[^']*')`)
	diagnosticAssignmentRE = regexp.MustCompile(`(?i)((?:[A-Z0-9_-]*(?:TOKEN|SECRET|PASSWORD|API[_-]?KEY|PRIVATE[_-]?KEY)[A-Z0-9_-]*)["']?\s*[=:]\s*)[^\r\n]+`)
	bearerRE               = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]{16,}`)
	knownTokenRE           = regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{20,}|gh[pousr]_[A-Za-z0-9]{20,}|xox[baprs]-[A-Za-z0-9-]{20,})\b`)
)

func SanitizeDiff(diff string) (Result, error) {
	if privateKeyRE.MatchString(diff) {
		return Result{}, ErrPrivateKey
	}
	sections := splitDiff(diff)
	result := Result{}
	var kept []string
	for _, section := range sections {
		name := sectionFilename(section)
		if name != "" && SensitiveFilename(name) {
			result.ExcludedFiles = append(result.ExcludedFiles, name)
			continue
		}
		kept = append(kept, section)
	}
	content := strings.Join(kept, "")
	result.Content, result.Redactions = RedactText(content)
	return result, nil
}

// RedactText removes common secret shapes without rejecting the input. It is
// suitable for bounded local diagnostics that must never expose raw stderr.
func RedactText(text string) (string, int) {
	text, n0 := replaceCount(text, privateKeyRE, `[REDACTED PRIVATE KEY]`)
	text, n1 := replaceCount(text, assignmentRE, `${1}[REDACTED]`)
	text, n2 := replaceCount(text, bearerRE, `${1}[REDACTED]`)
	text, n3 := replaceCount(text, knownTokenRE, `[REDACTED]`)
	return text, n0 + n1 + n2 + n3
}

// RedactDiagnostic also handles secret assignments embedded in prose or JSON.
func RedactDiagnostic(text string) string {
	text, _ = RedactText(text)
	return diagnosticAssignmentRE.ReplaceAllString(text, `${1}[REDACTED]`)
}

func SensitiveFilename(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if base == ".env" || strings.HasPrefix(base, ".env.") {
		return true
	}
	if strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") || strings.HasSuffix(base, ".p12") || strings.HasSuffix(base, ".pfx") {
		return true
	}
	return strings.Contains(base, "secret") || strings.Contains(base, "credential") || strings.Contains(base, "token")
}

func FilterUntracked(files []string) (safe, excluded []string) {
	for _, f := range files {
		if SensitiveFilename(f) {
			excluded = append(excluded, f)
		} else {
			safe = append(safe, f)
		}
	}
	return safe, excluded
}

func splitDiff(diff string) []string {
	if diff == "" {
		return nil
	}
	indices := regexp.MustCompile(`(?m)^diff --git `).FindAllStringIndex(diff, -1)
	if len(indices) == 0 {
		return []string{diff}
	}
	sections := make([]string, 0, len(indices)+1)
	if indices[0][0] > 0 {
		sections = append(sections, diff[:indices[0][0]])
	}
	for i, idx := range indices {
		end := len(diff)
		if i+1 < len(indices) {
			end = indices[i+1][0]
		}
		sections = append(sections, diff[idx[0]:end])
	}
	return sections
}

func sectionFilename(section string) string {
	line := strings.SplitN(section, "\n", 2)[0]
	const prefix = "diff --git a/"
	if !strings.HasPrefix(line, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(line, prefix)
	if i := strings.Index(rest, " b/"); i >= 0 {
		return rest[i+3:]
	}
	return ""
}

func replaceCount(input string, re *regexp.Regexp, replacement string) (string, int) {
	return re.ReplaceAllString(input, replacement), len(re.FindAllStringIndex(input, -1))
}
