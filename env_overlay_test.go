package smplkit

import "testing"

func TestOverlayString(t *testing.T) {
	raw := map[string]interface{}{"url": "https://x", "n": 1.0}
	if got := overlayString(raw, "url"); got != "https://x" {
		t.Errorf("overlayString present = %q, want https://x", got)
	}
	if got := overlayString(raw, "missing"); got != "" {
		t.Errorf("overlayString missing = %q, want empty", got)
	}
	// Present but wrong type reads as empty (not overridden).
	if got := overlayString(raw, "n"); got != "" {
		t.Errorf("overlayString non-string = %q, want empty", got)
	}
}

func TestOverlayStringPtr(t *testing.T) {
	raw := map[string]interface{}{"body": "", "n": 1.0}
	got := overlayStringPtr(raw, "body")
	if got == nil || *got != "" {
		t.Errorf("overlayStringPtr present-empty = %v, want pointer to empty string", got)
	}
	if got := overlayStringPtr(raw, "missing"); got != nil {
		t.Errorf("overlayStringPtr missing = %v, want nil", got)
	}
	if got := overlayStringPtr(raw, "n"); got != nil {
		t.Errorf("overlayStringPtr non-string = %v, want nil", got)
	}
}

func TestOverlayBool(t *testing.T) {
	raw := map[string]interface{}{"enabled": true, "x": "no"}
	if !overlayBool(raw, "enabled") {
		t.Error("overlayBool present-true = false, want true")
	}
	if overlayBool(raw, "missing") {
		t.Error("overlayBool missing = true, want false")
	}
	if overlayBool(raw, "x") {
		t.Error("overlayBool non-bool = true, want false")
	}
}

func TestOverlayBoolPtr(t *testing.T) {
	raw := map[string]interface{}{"tls_verify": false, "x": "no"}
	got := overlayBoolPtr(raw, "tls_verify")
	if got == nil || *got != false {
		t.Errorf("overlayBoolPtr present-false = %v, want pointer to false", got)
	}
	if got := overlayBoolPtr(raw, "missing"); got != nil {
		t.Errorf("overlayBoolPtr missing = %v, want nil", got)
	}
	if got := overlayBoolPtr(raw, "x"); got != nil {
		t.Errorf("overlayBoolPtr non-bool = %v, want nil", got)
	}
}

func TestOverlayInt(t *testing.T) {
	// float64 is how JSON-decoded numbers arrive over the wire; int is how an
	// in-memory overlay (built by *ToWire) carries them on a direct round-trip.
	raw := map[string]interface{}{"f": 30.0, "i": 45, "s": "x"}
	if got := overlayInt(raw, "f"); got != 30 {
		t.Errorf("overlayInt float64 = %d, want 30", got)
	}
	if got := overlayInt(raw, "i"); got != 45 {
		t.Errorf("overlayInt int = %d, want 45", got)
	}
	if got := overlayInt(raw, "missing"); got != 0 {
		t.Errorf("overlayInt missing = %d, want 0", got)
	}
	if got := overlayInt(raw, "s"); got != 0 {
		t.Errorf("overlayInt non-number = %d, want 0", got)
	}
}

func TestOverlayHeaders(t *testing.T) {
	raw := map[string]interface{}{
		"enabled":           true,        // non-header key, skipped
		"headers.X-Api-Key": "secret",    // plain header leaf
		"headers.X-Foo.Bar": "dotted",    // dotted name kept whole (split on FIRST dot)
		"headers.Empty":     "",          // empty value is a real override
		"headers.":          "no-name",   // empty header name, skipped
		"headers.Bad":       42,          // non-string value, skipped
		"url":               "https://x", // non-header key, skipped
	}
	got := overlayHeaders(raw)
	if len(got) != 3 {
		t.Fatalf("overlayHeaders len = %d (%v), want 3", len(got), got)
	}
	if got["X-Api-Key"] != "secret" {
		t.Errorf("X-Api-Key = %q, want secret", got["X-Api-Key"])
	}
	if got["X-Foo.Bar"] != "dotted" {
		t.Errorf("X-Foo.Bar = %q, want dotted", got["X-Foo.Bar"])
	}
	if v, ok := got["Empty"]; !ok || v != "" {
		t.Errorf("Empty = %q (ok=%t), want empty override present", v, ok)
	}
	if _, ok := got["Bad"]; ok {
		t.Error("non-string header leaf should be skipped")
	}

	// No header leaves at all → nil map.
	if got := overlayHeaders(map[string]interface{}{"enabled": true}); got != nil {
		t.Errorf("overlayHeaders no-headers = %v, want nil", got)
	}
}
