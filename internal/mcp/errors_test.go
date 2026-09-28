package mcp

import (
	"errors"
	"strings"
	"testing"
)

func TestSentinelsDistinct(t *testing.T) {
	sentinels := []error{
		ErrProtocol, ErrInvalidMessage, ErrUnsupportedVersion,
		ErrTooLarge, ErrInvalidTool, ErrMismatch, ErrInvalidConfig,
		ErrTransport, ErrClosed,
	}
	seen := map[string]bool{}
	for _, s := range sentinels {
		if seen[s.Error()] {
			t.Errorf("duplicate sentinel message %q", s.Error())
		}
		seen[s.Error()] = true
	}
}

func TestBoundedDetail(t *testing.T) {
	if got := BoundedDetail(""); got != "" {
		t.Errorf("empty detail = %q, want empty", got)
	}
	short := "tool skipped: bad schema"
	if got := BoundedDetail(short); got != short {
		t.Errorf("short detail changed: %q", got)
	}
	long := strings.Repeat("x", MaxErrorDetailRunes+500)
	got := BoundedDetail(long)
	if n := len([]rune(got)); n > MaxErrorDetailRunes+len(TruncationMarker) {
		t.Errorf("bounded detail of %d runes exceeds cap", n)
	}
	if !strings.HasSuffix(got, TruncationMarker) {
		t.Error("long detail missing truncation marker")
	}
	// Multibyte content is cut on a rune boundary.
	multi := strings.Repeat("世", MaxErrorDetailRunes+10)
	if got := BoundedDetail(multi); len([]rune(got)) > MaxErrorDetailRunes+len(TruncationMarker) {
		t.Errorf("multibyte detail of %d runes exceeds cap", len([]rune(got)))
	}
}

func TestErrorWrappingPreserved(t *testing.T) {
	_, err := Negotiate("bogus")
	if !errors.Is(err, ErrUnsupportedVersion) {
		t.Errorf("version err = %v, want ErrUnsupportedVersion", err)
	}
	_, err = ParseMessage([]byte(`[]`))
	if !errors.Is(err, ErrProtocol) {
		t.Errorf("batch err = %v, want ErrProtocol", err)
	}
	big := make([]byte, MaxMessageBytes+1)
	if _, err = ParseMessage(big); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversize err = %v, want ErrTooLarge", err)
	}
}
