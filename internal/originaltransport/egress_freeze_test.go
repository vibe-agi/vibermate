package originaltransport_test

import (
	"bytes"
	"io"
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originaltransport"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

func originalResolved(t testing.TB, policy egressnetwork.Policy) egressprofile.Resolved {
	t.Helper()
	normalized, err := policy.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	profile := egressprofile.Direct()
	if normalized != profile.Policy {
		profile.ID, profile.DisplayName, profile.Policy = "profile.original-fixture", "Synthetic original fixture", normalized
	}
	resolved, err := egressprofile.RestoreResolved(profile, egressprofile.SelectionFlow, 0)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestOriginalTransportRecordsAndSendsSameFrozenGlobalExit(t *testing.T) {
	profile := egressprofile.Direct()
	profile.ID, profile.DisplayName = "profile.original-synthetic", "Synthetic proxy"
	profile.Policy.Proxy = egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxySOCKS5, Endpoint: "127.0.0.1:7890"}
	selected, err := egressprofile.RestoreResolved(profile, egressprofile.SelectionGlobal, 9)
	if err != nil {
		t.Fatal(err)
	}
	origin, _ := originidentity.ParseProviderOrigin("https://original.synthetic")
	request, err := originaltransport.NewRequest(originaltransport.RequestOptions{RequestID: "original-egress", ConnectionID: "original-connection", ParentID: "original-parent", Kind: offlinehold.EgressAuxiliary, Origin: origin, Method: http.MethodGet, Path: "/models", PayloadClass: protocolspec.OperationPayloadNone, EgressPolicy: profile.Policy, ResolvedEgress: selected})
	if err != nil {
		t.Fatal(err)
	}
	recorder := &auditRecorder{}
	wire := &roundTripper{response: &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewBufferString("ok"))}}
	gate := &recordingCoordinator{}
	client, err := originaltransport.New(originaltransport.Options{Coordinator: gate, Transport: wire, Audit: recorder})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownClient(t, client)
	response, err := client.Do(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.Copy(io.Discard, response.Body); err != nil {
		t.Fatal(err)
	}
	if err = response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	started, terminal := recorder.snapshot()
	if len(started) != 1 || len(terminal) != 1 {
		t.Fatalf("started=%d terminal=%d", len(started), len(terminal))
	}
	decision := started[0].Decision()
	if decision.ProxyID != "profile.original-synthetic" || decision.ProxyRevision != 1 || decision.SelectionSource != egressprofile.SelectionGlobal || decision.DefaultRevision != 9 {
		t.Fatalf("frozen global mislabeled in real audit: %+v", decision)
	}
	if terminal[0].Decision() != decision || wire.Policy() != profile.Policy || gate.Request().Target.EgressPolicy != profile.Policy {
		t.Fatal("audit/probe/wire selection diverged")
	}
}
