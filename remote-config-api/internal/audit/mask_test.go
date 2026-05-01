package audit

import "testing"

func TestMaskValue(t *testing.T) {
	if got := MaskValue("plain", false); got != "plain" {
		t.Fatalf("expected plain value, got %q", got)
	}
	if got := MaskValue("secret", true); got != MaskedValue {
		t.Fatalf("expected masked secret, got %q", got)
	}
	if got := MaskValue("", true); got != "" {
		t.Fatalf("expected empty secret to stay empty, got %q", got)
	}
}

