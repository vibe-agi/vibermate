package providertransport

import (
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originidentity"
)

func TestMetadataProbeRejectsAlteredClosedProducerBeforeEgress(t *testing.T) {
	for _, edge := range []string{"origin", "port", "path", "query", "POST", "target-reference", "account", "credential", "captured", "body", "content-type"} {
		t.Run(edge, func(t *testing.T) {
			gate := newStartedGate(t)
			audit := &runtimeAuditRecorder{}
			transport := &runtimeTransportStub{audit: audit}
			client := newRuntimeFetchClient(t, gate, transport, audit)
			origin, err := originidentity.ParseProviderOrigin("https://models.dev")
			if err != nil {
				t.Fatal(err)
			}
			target, err := NewTarget(origin)
			if err != nil {
				t.Fatal(err)
			}
			spec := runtimeFetchSpec{purpose: egressaudit.PurposeModelMetadataDirectory, targetRef: "runtime.models-dev", target: target, method: http.MethodGet, relativePath: "/models.json"}
			switch edge {
			case "origin", "port":
				raw := "https://unapproved.invalid"
				if edge == "port" {
					raw = "https://models.dev:444"
				}
				origin, err = originidentity.ParseProviderOrigin(raw)
				if err != nil {
					t.Fatal(err)
				}
				spec.target, err = NewTarget(origin)
				if err != nil {
					t.Fatal(err)
				}
			case "path":
				spec.relativePath = "/other.json"
			case "query":
				spec.relativePath = "/models.json?extra=true"
			case "POST":
				spec.method = http.MethodPost
			case "target-reference":
				spec.targetRef = "some-model-route"
			case "account":
				spec.account = testAccountRef()
			case "credential":
				spec.credential = testRuntimeDiscoveryCredential(t, "fixture-realm")
			case "captured":
				spec.captured = &capturedAccountRead{}
			case "body":
				spec.body = []byte("not permitted")
			case "content-type":
				spec.contentType = "application/json"
			}
			response, err := client.fetchRuntimeJSON(t.Context(), spec)
			if response != nil {
				response.Body.Close()
			}
			if err == nil || transport.calls != 0 || audit.appendCount() != 0 {
				t.Fatalf("altered metadata producer reached transport/audit: edge=%s error=%t transport=%d audit=%d", edge, err != nil, transport.calls, audit.appendCount())
			}
			if snapshot := gate.Snapshot(); snapshot.ActiveActions != 0 || snapshot.ActiveEgress != 0 || snapshot.QueuedRequests != 0 {
				t.Fatal("invalid metadata producer retained admission")
			}
		})
	}
}

func TestMetadataAndPricesProbeHasOnlyOriginReachabilityIdentity(t *testing.T) {
	gate := newStartedGate(t)
	client := newRuntimeFetchClient(t, gate, &runtimeTransportStub{}, &runtimeAuditRecorder{})
	origin, _ := originidentity.ParseProviderOrigin("https://models.dev")
	target, _ := NewTarget(origin)
	resolved, err := client.FreezeLoginEgress(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/models.json", "/api.json"} {
		actual, err := client.runtimeProbeTarget(runtimeFetchSpec{purpose: egressaudit.PurposeModelMetadataDirectory, targetRef: "runtime.models-dev", target: target, method: http.MethodGet, relativePath: path, resolved: resolved})
		if err != nil || actual.Kind != offlinehold.EgressAuxiliary || actual.Transport != offlinehold.ProbeTransportStrictTLS || actual.TargetRef != "https://models.dev" || actual.NetworkOrigin != "https://models.dev" || actual.HTTPAuthority != "models.dev" || actual.TLSServerName != "models.dev" || actual.PlanRevision != 0 || actual.PlanDigest != "" || actual.TargetAccess != 0 || actual.EgressPolicy != resolved.Profile().Policy {
			t.Fatalf("metadata probe identity: %+v err=%v", actual, err)
		}
	}
}
