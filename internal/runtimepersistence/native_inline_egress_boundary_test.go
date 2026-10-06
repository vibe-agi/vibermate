package runtimepersistence

import (
	"encoding/json"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"strings"
	"testing"
)

func TestNativeEffectiveEvidenceBoundCoversMaximumNormalizedAndPublishedProfile(t *testing.T) {
	store := openTestStore(t, t.TempDir()+"/runtime.db")
	defer shutdownTestStore(t, store)
	prefix := "https://dns.synthetic/"
	profile := egressprofile.Direct()
	profile.ID, profile.DisplayName = egressprofile.ID(strings.Repeat("a", 128)), strings.Repeat("&", 256)
	host := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)
	profile.Policy.Proxy = egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxySOCKS5, Endpoint: host + ":65535"}
	profile.Policy.Resolver = egressnetwork.ResolverPolicy{Kind: egressnetwork.ResolverDoH, DoHURL: prefix + strings.Repeat("&", 2048-len(prefix)), Transport: egressnetwork.ResolverTransportProxy}
	choice := authoredEgress(t, profile)
	encoded, err := json.Marshal(profile)
	if err != nil || len(encoded) > nativetool.MaxEffectiveProfileJSONBytes {
		t.Fatal("maximum normalized profile exceeds evidence bound", err)
	}
	manager := newEnvironmentManager(t, store)
	value := environment.Environment{ID: "inline-legacy", Name: "Synthetic legacy", State: environment.StateActive, Revision: 1, ContentRecording: environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, ClientToolAccess: &environment.ClientToolAccessPolicy{Enabled: true, Route: &environment.ClientToolRoute{ID: "native.inline", Revision: 1, TargetOrigin: runtimeProviderOrigin(t, "https://chatgpt.com"), Path: environment.ClientToolMCPPath, CredentialOwner: environment.ClientToolCredentialOriginal, Contract: environment.ClientToolContractCodexLegacy, EgressProfile: choice}}}
	draft, err := manager.SaveDraft(t.Context(), environment.DraftCommand{Candidate: value})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := manager.Preview(t.Context(), value.ID, draft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Publish(t.Context(), preview); err != nil {
		t.Fatal(err)
	}
	stored, err := manager.GetRevision(t.Context(), value.ID, 1)
	if err != nil || stored.Aggregate().ClientToolAccess.Route.EgressProfile != choice {
		t.Fatal("largest normalized inline profile did not roundtrip real Store", err)
	}
	t.Logf("max normalized full-profile JSON=%d cap=%d", len(encoded), nativetool.MaxEffectiveProfileJSONBytes)
	profiles, err := egressprofile.NewManager(store.EgressProfileRepository(), nil)
	if err != nil {
		t.Fatal(err)
	}
	// Find the largest number of escaped legal URL characters within the actual8192-byte policy column.
	for {
		policyJSON, err := json.Marshal(profile.Policy)
		if err != nil {
			t.Fatal(err)
		}
		if len(policyJSON) <= 8192 {
			break
		}
		profile.Policy.Resolver.DoHURL = strings.TrimSuffix(profile.Policy.Resolver.DoHURL, "&")
	}
	published, err := profiles.Publish(t.Context(), egressprofile.PublishCommand{ID: profile.ID, DisplayName: profile.DisplayName, Policy: profile.Policy})
	if err != nil {
		t.Fatal(err)
	}
	actual, err := profiles.GetRevision(t.Context(), published.ID, published.Revision)
	if err != nil || !actual.Equal(published) {
		t.Fatal("maximum published profile not restored", err)
	}
	fullJSON, err := json.Marshal(actual)
	if err != nil || len(fullJSON) > nativetool.MaxEffectiveProfileJSONBytes {
		t.Fatal("published profile exceeds native bound", err)
	}
	var restored egressprofile.Choice
	if err = json.Unmarshal(fullJSON, &restored); err != nil || restored != authoredEgress(t, actual) {
		t.Fatal("published full profile strict roundtrip failed", err)
	}
	policyJSON, _ := json.Marshal(actual.Policy)
	t.Logf("max published policy JSON=%d full-profile JSON=%d", len(policyJSON), len(fullJSON))
	overflow := profile.Policy
	overflow.Resolver.DoHURL += "&"
	if _, err := profiles.Publish(t.Context(), egressprofile.PublishCommand{ID: profile.ID, ExpectedRevision: 1, DisplayName: profile.DisplayName, Policy: overflow}); err == nil {
		t.Fatal("policy over actual8192-byte publication bound accepted")
	}
	if got, err := profiles.GetRevision(t.Context(), published.ID, 1); err != nil || !got.Equal(published) {
		t.Fatal("failed overflow altered previous published value", err)
	}
	for _, url := range []string{"https://dns.synthetic/" + strings.Repeat("q", 2048), "https://dns.synthetic/path?q=1"} {
		invalid := profile
		invalid.Policy.Resolver.DoHURL = url
		if _, err := egressprofile.Explicit(invalid); err == nil {
			t.Fatal("overlong/query DoH URL accepted")
		}
	}
}
