package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

type StreamResult struct {
	SessionID        string
	StructuredOutput json.RawMessage
	ResultText       string
	EventCount       int
}

func ParseStream(r io.Reader, maxBytes int64, debug func(string)) (StreamResult, error) {
	if maxBytes <= 0 {
		maxBytes = 8 * 1024 * 1024
	}
	lr := &io.LimitedReader{R: r, N: maxBytes + 1}
	scanner := bufio.NewScanner(lr)
	maxToken := int(maxBytes)
	if maxToken > 16*1024*1024 {
		maxToken = 16 * 1024 * 1024
	}
	scanner.Buffer(make([]byte, 64*1024), maxToken)
	var out StreamResult
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		out.EventCount++
		var value any
		if err := json.Unmarshal(line, &value); err != nil {
			if debug != nil {
				debug(string(line))
			}
			continue
		}
		if out.SessionID == "" {
			out.SessionID = findString(value, "session_id")
		}
		m, _ := value.(map[string]any)
		if m["type"] == "result" {
			if raw, ok := m["structured_output"]; ok {
				if b, err := json.Marshal(raw); err == nil && string(b) != "null" {
					out.StructuredOutput = b
				}
			}
			if result, ok := m["result"].(string); ok {
				out.ResultText = result
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return out, fmt.Errorf("read claude stream: %w", err)
	}
	if lr.N <= 0 {
		return out, fmt.Errorf("%w: maximum is %d bytes", ErrOutputTooLarge, maxBytes)
	}
	if len(out.StructuredOutput) == 0 && out.ResultText != "" {
		var raw json.RawMessage
		if json.Unmarshal([]byte(out.ResultText), &raw) == nil {
			out.StructuredOutput = raw
		}
	}
	return out, nil
}

func findString(value any, key string) string {
	switch v := value.(type) {
	case map[string]any:
		if s, ok := v[key].(string); ok && s != "" {
			return s
		}
		for _, child := range v {
			if s := findString(child, key); s != "" {
				return s
			}
		}
	case []any:
		for _, child := range v {
			if s := findString(child, key); s != "" {
				return s
			}
		}
	}
	return ""
}
