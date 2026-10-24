package providertransport

import (
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/providerauth"
)

func TestPrepareProviderHeadersRemovesFieldsNamedOnEveryConnectionLine(t *testing.T) {
	source := http.Header{
		"Connection":   {"keep-alive", "X-Second-Hop"},
		"X-Second-Hop": {"hop"},
		"X-End":        {"kept"},
	}
	got := prepareProviderHeaders(source, providerauth.CredentialManaged)
	if got.Get("X-Second-Hop") != "" || got.Get("X-End") != "kept" {
		t.Fatalf("prepareProviderHeaders() = %v, want X-Second-Hop removed and X-End kept", got)
	}
}
