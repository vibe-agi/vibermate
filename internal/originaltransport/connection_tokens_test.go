package originaltransport

import (
	"net/http"
	"testing"
)

func TestSanitizeHeadersRemovesFieldsNamedOnEveryConnectionLine(t *testing.T) {
	source := http.Header{
		"Connection":   {"keep-alive", "X-Second-Hop"},
		"X-Second-Hop": {"hop"},
		"X-End":        {"kept"},
	}
	got := sanitizeHeaders(source)
	if got.Get("X-Second-Hop") != "" || got.Get("X-End") != "kept" {
		t.Fatalf("sanitizeHeaders() = %v, want X-Second-Hop removed and X-End kept", got)
	}
}
