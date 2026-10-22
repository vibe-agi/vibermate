package upstreamservice

import (
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/providerauth"
)

func TestModelDirectoryIsAClosedBoundedAccountRead(t *testing.T) {
	origin, _ := originidentity.ParseProviderOrigin("https://chatgpt.com/backend-api/codex")
	read, err := ResolveRead(origin, "openai-codex-models-probe")
	if err != nil {
		t.Fatal(err)
	}
	if read.Path() != "/backend-api/codex/models" || read.RequiresHistoryPermission() {
		t.Fatal("wrong model read contract")
	}
	for _, query := range []string{"", "client_version=0.163.0", "client_version=0.164.1"} {
		bound, err := read.WithRawQuery(query)
		if err != nil || bound.RawQuery() != query || bound.Validate() != nil {
			t.Fatalf("query=%q err=%v", query, err)
		}
	}
	for _, query := range []string{"account_id=other", "client_version=0.163.0&account_id=other", "client_version=0.163.0&client_version=0.164.0", "client_version=", "client_version=abc", "client_version=0.163.0%26token%3Dsecret", "client_version=0.163", "client_version=%ff"} {
		if _, err := read.WithRawQuery(query); err == nil {
			t.Fatalf("admitted undeclared query %q", query)
		}
	}
	quota, err := ResolveRead(origin, CodexRateLimits)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := quota.WithRawQuery("client_version=0.163.0"); err == nil {
		t.Fatal("model query broadened quota contract")
	}
	body := []byte(`{"models":[{"slug":"m","available_access_programs":{"cyber":["standard","daybreak_blue"]},"service_tiers":[{"id":"priority"}],"future":{"x":1}}],"unknown":"preserve"}`)
	if _, err := read.Project(body); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{`null`, `{"models":null}`, `{"models":[null]}`, `{"models":{}}`, `{"models":[],"Models":[]}`, `{"models":[{"slug":"m","slug":"other"}]}`, strings.Repeat(" ", MaxCodexModelsBodyBytes+1)} {
		if _, err := read.Project([]byte(invalid)); err == nil {
			t.Fatal("accepted invalid catalog")
		}
	}
}

func TestOfficialAPIModelDirectoryDescriptorKeepsSourceScope(t *testing.T) {
	for _, raw := range []string{"https://api.openai.com", "https://api.openai.com/v1"} {
		source, _ := originidentity.ParseProviderOrigin(raw)
		read, err := ResolveAccountRead(source, providerauth.StaticHeaderDriverRef(), CodexModels)
		if err != nil || read.Validate() != nil || read.SourceOrigin() != source || read.Origin().String() != "https://chatgpt.com/backend-api/codex" || read.Path() != "/backend-api/codex/models" {
			t.Fatalf("invalid sealed catalog: %+v %v", read, err)
		}
		for _, operation := range []string{CodexRateLimits, CodexUsageHistory, CodexResetCreditDetails} {
			if _, err := ResolveAccountRead(source, providerauth.StaticHeaderDriverRef(), operation); err == nil {
				t.Fatalf("cross-origin %s admitted", operation)
			}
		}
		if _, err := ResolveAccountRead(source, providerauth.CodexOAuthDriverRef(), CodexModels); err == nil {
			t.Fatal("API OAuth admitted")
		}
		tampered := read
		tampered.origin = source
		if tampered.Validate() == nil {
			t.Fatal("metadata destination changed")
		}
		tampered = read
		tampered.path = "/backend-api/wham/usage"
		if tampered.Validate() == nil {
			t.Fatal("metadata path changed")
		}
	}
	for _, raw := range []string{"https://api.openai.com:8443", "https://api.openai.com/custom", "https://api.openai.com.example", "https://relay.example/v1"} {
		source, _ := originidentity.ParseProviderOrigin(raw)
		if _, err := ResolveAccountRead(source, providerauth.StaticHeaderDriverRef(), CodexModels); err == nil {
			t.Fatalf("custom origin admitted: %s", raw)
		}
	}
}
