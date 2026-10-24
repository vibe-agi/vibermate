package hopbyhop

import (
	"net/http"
	"testing"
)

func TestRemoveConnectionNamedReadsEveryLine(t *testing.T) {
	header := http.Header{
		"Connection":   {"keep-alive, X-First", " X-Second ,,"},
		"Keep-Alive":   {"timeout=5"},
		"X-First":      {"1"},
		"X-Second":     {"2"},
		"X-End-To-End": {"kept"},
	}
	RemoveConnectionNamed(header)
	for _, name := range []string{"Keep-Alive", "X-First", "X-Second"} {
		if header.Get(name) != "" {
			t.Fatalf("%s survived: %v", name, header)
		}
	}
	if header.Get("X-End-To-End") != "kept" || len(header.Values("Connection")) != 2 {
		t.Fatalf("unrelated fields changed: %v", header)
	}
}
