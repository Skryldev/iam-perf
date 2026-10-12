package telemetry

import (
	"bytes"

	"strings"
	"testing"
)

func TestRedactingHandler_Text(t *testing.T) {
	var buf bytes.Buffer
	l := NewLogger("debug", "text", &buf)
	l.Info("verify failed for token 80ff55a0-afb0-416c-a735-ab027592738d",
		"detail", "jwt eyJhbGciOiJIUzI1NiJ9.abc.def")
	out := buf.String()
	if strings.Contains(out, "80ff55a0") || strings.Contains(out, "eyJ") {
		t.Fatalf("token leaked into logs: %s", out)
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Fatalf("redaction marker missing: %s", out)
	}
}

func TestRedactingHandler_JSON(t *testing.T) {
	var buf bytes.Buffer
	l := NewLogger("info", "json", &buf)
	l.Error("auth error", "msg", "hex aabbccddeeff00112233445566778899 end")
	out := buf.String()
	if strings.Contains(out, "aabbcc") {
		t.Fatalf("token leaked: %s", out)
	}
	if !strings.Contains(out, `"level":"ERROR"`) {
		t.Fatalf("json format broken: %s", out)
	}
}

func TestLevels(t *testing.T) {
	var buf bytes.Buffer
	l := NewLogger("warn", "text", &buf)
	l.Debug("debug line")
	l.Info("info line")
	l.Warn("warn line")
	out := buf.String()
	if strings.Contains(out, "debug line") || strings.Contains(out, "info line") {
		t.Fatalf("level filtering broken: %s", out)
	}
	if !strings.Contains(out, "warn line") {
		t.Fatalf("warn missing: %s", out)
	}
	// Default fallback: unknown level acts as info (no panic).
	var buf2 bytes.Buffer
	l2 := NewLogger("bogus", "text", &buf2)
	l2.Info("fallback line")
	if buf2.Len() == 0 {
		t.Error("unknown level must fall back to info")
	}
}
