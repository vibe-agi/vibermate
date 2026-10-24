package loopbackproxy

import (
	"net/http"
	"testing"
)

func TestHopByHopHelpersRemoveFieldsNamedOnEveryConnectionLine(t *testing.T) {
	source := http.Header{
		"Connection":   {"keep-alive", "X-Second-Hop"},
		"X-Second-Hop": {"hop"},
		"X-End":        {"kept"},
	}
	forwarded := forwardableHeaders(source)
	if forwarded.Get("X-Second-Hop") != "" || forwarded.Get("X-End") != "kept" {
		t.Fatalf("forwardableHeaders() = %v, want X-Second-Hop removed and X-End kept", forwarded)
	}
	copied := http.Header{}
	copyResponseHeaders(copied, source)
	if copied.Get("X-Second-Hop") != "" || copied.Get("X-End") != "kept" {
		t.Fatalf("copyResponseHeaders() = %v, want X-Second-Hop removed and X-End kept", copied)
	}
}
