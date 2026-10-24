package exchange

import (
	"net/http"
	"testing"
)

func TestResponseEnvelopeRemovesFieldsNamedOnEveryConnectionLine(t *testing.T) {
	envelope, err := NewResponseEnvelope(ResponseModeJSON, http.StatusOK, http.Header{
		"Connection":   {"keep-alive", "X-Second-Hop"},
		"X-Second-Hop": {"hop"},
		"X-End":        {"kept"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := envelope.Headers()
	if got.Get("X-Second-Hop") != "" || got.Get("X-End") != "kept" {
		t.Fatalf("Headers() = %v, want X-Second-Hop removed and X-End kept", got)
	}
}
