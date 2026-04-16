package debug

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// parseDebugEnv — env-string parsing
// ---------------------------------------------------------------------------

func TestParseDebugEnv_Truthy(t *testing.T) {
	truthy := []string{"1", "true", "TRUE", "True", "yes", "YES", "Yes"}
	for _, v := range truthy {
		if !parseDebugEnv(v) {
			t.Errorf("parseDebugEnv(%q) = false, want true", v)
		}
	}
}

func TestParseDebugEnv_Falsy(t *testing.T) {
	falsy := []string{"0", "false", "FALSE", "no", "NO", "", "  ", "2", "on", "enable"}
	for _, v := range falsy {
		if parseDebugEnv(v) {
			t.Errorf("parseDebugEnv(%q) = true, want false", v)
		}
	}
}

func TestParseDebugEnv_Whitespace(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"  1  ", true},
		{"  true  ", true},
		{"  false  ", false},
	}
	for _, tc := range cases {
		got := parseDebugEnv(tc.input)
		if got != tc.want {
			t.Errorf("parseDebugEnv(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// IsEnabled — reports the cached state
// ---------------------------------------------------------------------------

func TestIsEnabled_ReflectsDebugEnabled(t *testing.T) {
	orig := debugEnabled
	defer func() { debugEnabled = orig }()

	debugEnabled = true
	if !IsEnabled() {
		t.Error("IsEnabled() = false, want true when debugEnabled=true")
	}

	debugEnabled = false
	if IsEnabled() {
		t.Error("IsEnabled() = true, want false when debugEnabled=false")
	}
}

// ---------------------------------------------------------------------------
// Debug — no-op when disabled
// ---------------------------------------------------------------------------

func TestDebug_NoOpWhenDisabled(t *testing.T) {
	orig := debugEnabled
	debugEnabled = false
	defer func() { debugEnabled = orig }()

	// Redirect stderr.
	r, w, _ := os.Pipe()
	oldStderr := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = oldStderr }()

	Debug("websocket", "this should not appear")

	w.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)

	if buf.Len() > 0 {
		t.Errorf("Debug wrote to stderr when disabled: %q", buf.String())
	}
}

// ---------------------------------------------------------------------------
// Debug — output format when enabled
// ---------------------------------------------------------------------------

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStderr := os.Stderr
	os.Stderr = w

	fn()

	w.Close()
	os.Stderr = oldStderr

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	return buf.String()
}

func TestDebug_WritesToStderr(t *testing.T) {
	orig := debugEnabled
	debugEnabled = true
	defer func() { debugEnabled = orig }()

	out := captureStderr(t, func() {
		Debug("websocket", "connected")
	})
	if out == "" {
		t.Error("Debug wrote nothing to stderr when enabled")
	}
}

func TestDebug_PrefixFormat(t *testing.T) {
	orig := debugEnabled
	debugEnabled = true
	defer func() { debugEnabled = orig }()

	out := captureStderr(t, func() {
		Debug("websocket", "some message")
	})
	if !strings.HasPrefix(out, "[smplkit:websocket]") {
		t.Errorf("output does not start with [smplkit:websocket]: %q", out)
	}
}

func TestDebug_IncludesSubsystemTag(t *testing.T) {
	orig := debugEnabled
	debugEnabled = true
	defer func() { debugEnabled = orig }()

	out := captureStderr(t, func() {
		Debug("api", "GET /api/v1/loggers")
	})
	if !strings.Contains(out, "[smplkit:api]") {
		t.Errorf("output does not contain [smplkit:api]: %q", out)
	}
}

func TestDebug_IncludesMessage(t *testing.T) {
	orig := debugEnabled
	debugEnabled = true
	defer func() { debugEnabled = orig }()

	out := captureStderr(t, func() {
		Debug("lifecycle", "SmplClient.Close() called")
	})
	if !strings.Contains(out, "SmplClient.Close() called") {
		t.Errorf("output does not contain message: %q", out)
	}
}

func TestDebug_EndsWithNewline(t *testing.T) {
	orig := debugEnabled
	debugEnabled = true
	defer func() { debugEnabled = orig }()

	out := captureStderr(t, func() {
		Debug("adapter", "applying level DEBUG")
	})
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("output does not end with newline: %q", out)
	}
}

func TestDebug_ContainsISO8601Timestamp(t *testing.T) {
	orig := debugEnabled
	debugEnabled = true
	defer func() { debugEnabled = orig }()

	out := captureStderr(t, func() {
		Debug("resolution", "resolving level")
	})
	re := regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}`)
	if !re.MatchString(out) {
		t.Errorf("output does not contain ISO-8601 timestamp: %q", out)
	}
}

func TestDebug_OutputStructure(t *testing.T) {
	orig := debugEnabled
	debugEnabled = true
	defer func() { debugEnabled = orig }()

	out := captureStderr(t, func() {
		Debug("discovery", "new logger: foo.bar")
	})
	trimmed := strings.TrimRight(out, "\n")
	parts := strings.SplitN(trimmed, " ", 3)
	if len(parts) < 3 {
		t.Fatalf("expected at least 3 space-separated parts, got %d: %q", len(parts), out)
	}
	if parts[0] != "[smplkit:discovery]" {
		t.Errorf("parts[0] = %q, want [smplkit:discovery]", parts[0])
	}
	if !strings.Contains(parts[1], "T") {
		t.Errorf("parts[1] does not look like an ISO-8601 timestamp: %q", parts[1])
	}
	if !strings.HasSuffix(trimmed, "new logger: foo.bar") {
		t.Errorf("output does not end with message: %q", trimmed)
	}
}

func TestDebug_FormatArgs(t *testing.T) {
	orig := debugEnabled
	debugEnabled = true
	defer func() { debugEnabled = orig }()

	out := captureStderr(t, func() {
		Debug("api", "fetched %d flags", 42)
	})
	if !strings.Contains(out, "fetched 42 flags") {
		t.Errorf("output does not contain formatted message: %q", out)
	}
}

func TestDebug_AllSubsystems(t *testing.T) {
	orig := debugEnabled
	debugEnabled = true
	defer func() { debugEnabled = orig }()

	subsystems := []string{
		"lifecycle", "websocket", "api", "discovery",
		"resolution", "adapter", "registration",
	}
	for _, sub := range subsystems {
		out := captureStderr(t, func() {
			Debug(sub, "test")
		})
		want := fmt.Sprintf("[smplkit:%s]", sub)
		if !strings.Contains(out, want) {
			t.Errorf("subsystem %q: output does not contain %q: %q", sub, want, out)
		}
	}
}
