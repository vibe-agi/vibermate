package runtimepersistence

import (
	"bytes"
	"context"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/rawevidence"
)

// The scanner is the standing guard for INV-AUTHZ-REDACT. It has to be proven
// capable of failing before it is trusted to pass, so this test plants a
// credential value in a column the product writes and requires a hit.
func TestCredentialScanFindsAPlantedValueAndNamesItsColumn(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)

	// raw_query is chosen deliberately: a query-string API key is a real
	// credential-leak vector, it lands in a plaintext metadata column, and no
	// dialect this product currently supports uses it. The scanner is what would
	// catch it if one ever does.
	const planted = "api_key=sk-ant-planted-value"
	record := rawEvidenceRecordForTest(
		"writer-planted.1", 1, rawevidence.LayerClientIngress,
		[]byte("body"), []byte(`{"version":1,"headers":[]}`),
	)
	record.RawQuery = planted
	if err := store.RawEvidenceRepository().AppendBatch(
		context.Background(),
		[]rawevidence.StoredEnvelope{record},
		time.Now().UTC(),
	); err != nil {
		t.Fatal(err)
	}

	hits, err := scanColumnsForForbiddenValues(
		context.Background(), store.database, []string{planted},
	)
	if err != nil {
		t.Fatalf("scanColumnsForForbiddenValues() error = %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("the scanner did not find a planted credential value")
	}
	found := false
	for _, hit := range hits {
		if strings.Contains(hit, "runtime_raw_evidence_envelopes.raw_query") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the scanner did not name the offending column: %v", hits)
	}
}

// TestNoCredentialHeaderValueReachesAnyColumn is the standing guard for
// INV-AUTHZ-REDACT, and it has to cross the seam to be one: a real Observation
// carrying real credential headers, through the real writer, into the real
// SQLite store. Its earlier form built a StoredEnvelope by hand with nothing
// credential-bearing on it, so deleting the redaction entirely would not have
// failed it.
//
// The name says "header" because that is the whole of what it proves.
// TestBodyAndQuerySecretsAreRetainedAsTheClientSentThem covers the rest, where
// the property is the opposite one.
func TestNoCredentialHeaderValueReachesAnyColumn(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)

	writer, err := rawevidence.Open(context.Background(), rawevidence.Options{
		Repository: store.RawEvidenceRepository(),
		Random:     bytes.NewReader(bytes.Repeat([]byte{0x3F}, 4096)),
		Clock:      rawevidence.SystemClock{},
		Config:     rawevidence.DefaultConfig(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Shutdown(context.Background()) }()

	const (
		bearer = "Bearer sk-ant-api03-must-never-be-stored"
		apiKey = "sk-proj-must-never-be-stored"
		cookie = "session=must-never-be-stored"
	)
	watermark, err := writer.Observe(context.Background(), rawevidence.Observation{
		Context: rawevidence.Context{
			ScopeKind: rawevidence.ScopeManagedRun, ScopeID: "capture-scan",
			ExchangeID: "exchange-scan", ConnectionID: "connection-scan",
			EnvironmentID: "environment-scan", EnvironmentRevision: 1,
			ClientEndpointID: "endpoint-scan", ClientEndpointRevision: 1,
			ProtocolPlanID: "protocol-scan", ProtocolPlanRevision: 1,
			RouteID: "route-scan", RouteRevision: 1,
			Recording: rawevidence.RecordingFull, RetentionDays: 30,
		},
		Layer:      rawevidence.LayerClientIngress,
		ObservedAt: time.Now().UTC(),
		Method:     http.MethodPost,
		Scheme:     "https",
		Authority:  "api.anthropic.com",
		Path:       "/v1/messages",
		Headers: http.Header{
			"Authorization": []string{bearer},
			"X-Api-Key":     []string{apiKey},
			"Cookie":        []string{cookie},
			"Content-Type":  []string{"application/json"},
		},
		Body:           []byte(`{"model":"claude-opus-5","messages":[]}`),
		Complete:       true,
		Representation: "http_message",
		ContentType:    "application/json",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(context.Background(), watermark); err != nil {
		t.Fatal(err)
	}

	hits, err := scanColumnsForForbiddenValues(
		context.Background(), store.database, []string{bearer, apiKey, cookie},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("credential values reached stored columns: %v", hits)
	}

	// The field names are the evidence that a credential was present and removed,
	// so their absence would mean the guard passed for the wrong reason.
	envelopes, err := store.RawEvidenceRepository().ListExchange(
		context.Background(), "exchange-scan",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(envelopes) != 1 ||
		!slices.Equal(
			envelopes[0].RedactedCredentialFields,
			[]string{"Authorization", "Cookie", "X-Api-Key"},
		) {
		t.Fatalf("redaction evidence = %+v", envelopes)
	}
}

// The claim this product makes is now bounded: recognized credential *header*
// values are removed, and everything else is kept as the client sent it. Only
// the first half had a test. The second half is the half whose silent loss
// would be worse — a later change that scrubbed secrets out of bodies would
// destroy the evidence this product exists to preserve, and nothing would have
// failed. So the retained scope gets a guard of its own.
//
// It asserts through the read path rather than the column scanner on purpose:
// bodies are zstd-compressed in the chunk store, so a substring scan cannot see
// into them and would report absence for the wrong reason.
func TestBodyAndQuerySecretsAreRetainedAsTheClientSentThem(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)

	writer, err := rawevidence.Open(context.Background(), rawevidence.Options{
		Repository: store.RawEvidenceRepository(),
		Random:     bytes.NewReader(bytes.Repeat([]byte{0x5C}, 4096)),
		Clock:      rawevidence.SystemClock{},
		Config:     rawevidence.DefaultConfig(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Shutdown(context.Background()) }()

	const (
		headerSecret = "Bearer sk-ant-api03-removed-at-the-boundary"
		bodySecret   = "sk-proj-typed-into-the-prompt-by-the-user"
		querySecret  = "sk-ant-carried-in-the-query-string"
	)
	body := []byte(
		`{"model":"claude-opus-5","messages":[{"role":"user","content":` +
			`"deploy with ` + bodySecret + `"}]}`,
	)
	watermark, err := writer.Observe(context.Background(), rawevidence.Observation{
		Context: rawevidence.Context{
			ScopeKind: rawevidence.ScopeManagedRun, ScopeID: "capture-retained",
			ExchangeID: "exchange-retained", ConnectionID: "connection-retained",
			EnvironmentID: "environment-retained", EnvironmentRevision: 1,
			ClientEndpointID: "endpoint-retained", ClientEndpointRevision: 1,
			ProtocolPlanID: "protocol-retained", ProtocolPlanRevision: 1,
			RouteID: "route-retained", RouteRevision: 1,
			Recording: rawevidence.RecordingFull, RetentionDays: 30,
		},
		Layer:      rawevidence.LayerClientIngress,
		ObservedAt: time.Now().UTC(),
		Method:     http.MethodPost,
		Scheme:     "https",
		Authority:  "api.anthropic.com",
		Path:       "/v1/messages",
		RawQuery:   "api_key=" + querySecret,
		Headers: http.Header{
			"Authorization": []string{headerSecret},
			"Content-Type":  []string{"application/json"},
		},
		Body:           body,
		Complete:       true,
		Representation: "http_message",
		ContentType:    "application/json",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(context.Background(), watermark); err != nil {
		t.Fatal(err)
	}

	envelopes, err := store.RawEvidenceRepository().ListExchange(
		context.Background(), "exchange-retained",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(envelopes) != 1 {
		t.Fatalf("envelopes = %d, want 1", len(envelopes))
	}
	envelope := envelopes[0]

	payload, err := writer.ReadPayload(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload.Body, body) {
		t.Fatalf(
			"the stored body is not the observed body; a secret inside it was "+
				"rewritten or dropped:\n got %q\nwant %q",
			payload.Body, body,
		)
	}
	if envelope.RawQuery != "api_key="+querySecret {
		t.Fatalf(
			"raw_query = %q; the query the client sent was not retained",
			envelope.RawQuery,
		)
	}

	// The bounded half of the claim still holds: the header value is gone from
	// every column, and only its field name remains.
	hits, err := scanColumnsForForbiddenValues(
		context.Background(), store.database, []string{headerSecret},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("a credential header value reached stored columns: %v", hits)
	}
	if !slices.Equal(envelope.RedactedCredentialFields, []string{"Authorization"}) {
		t.Fatalf(
			"redaction evidence = %v, want [Authorization]",
			envelope.RedactedCredentialFields,
		)
	}
}
