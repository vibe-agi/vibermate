package rawevidence

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"
)

func redactionTestConfig() Config {
	return Config{
		MaximumQueueRecords: 8,
		MaximumQueueBytes:   1 << 20,
		MaximumBatchRecords: 8,
		MaximumBatchBytes:   1 << 20,
		FlushInterval:       time.Hour,
	}
}

func TestOpenFailsWhenTheDatabaseCannotSupplyARedactionSalt(t *testing.T) {
	t.Parallel()

	manager, err := Open(context.Background(), Options{
		Repository: &memoryRepository{
			saltErr: errors.New("redaction salt is unavailable"),
		},
		Random: bytes.NewReader(bytes.Repeat([]byte{0x41}, 4096)),
		Clock:  fixedClock{value: time.Unix(1_790_000_000, 0).UTC()},
		Config: redactionTestConfig(),
	})
	if err == nil {
		_ = manager.Shutdown(context.Background())
		t.Fatal("Open() succeeded without a redaction salt")
	}
}

func testRedactor(t *testing.T) Redactor {
	t.Helper()
	redactor, err := NewRedactor(bytes.Repeat([]byte{0xA5}, RedactionSaltBytes))
	if err != nil {
		t.Fatalf("NewRedactor() error = %v", err)
	}
	return redactor
}

func TestPayloadNeverRetainsACredentialHeaderValue(t *testing.T) {
	t.Parallel()

	headers := http.Header{}
	headers.Set("Authorization", "Bearer sk-ant-api03-do-not-store-me")
	headers.Set("Content-Type", "application/json")

	payload, redacted, err := payloadOf(
		Observation{Headers: headers},
		nil,
		testRedactor(t),
	)
	if err != nil {
		t.Fatalf("payloadOf() error = %v", err)
	}

	encoded, err := payload.MarshalMetadata()
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if bytes.Contains(encoded, []byte("sk-ant-api03-do-not-store-me")) {
		t.Fatal("payload retained a credential value")
	}
	if !slices.Equal(redacted, []string{"Authorization"}) {
		t.Fatalf("payloadOf reported %v as redacted", redacted)
	}
}

func TestPayloadNeverRetainsAnAccountProtectedHeaderValue(t *testing.T) {
	t.Parallel()

	headers := http.Header{
		"X-Relay-Tenant": []string{"private-team-a"},
		"Content-Type":   []string{"application/json"},
	}
	payload, redacted, err := payloadOf(
		Observation{
			Headers:              headers,
			ProtectedHeaderNames: []string{"X-Relay-Tenant"},
		},
		nil,
		testRedactor(t),
	)
	if err != nil {
		t.Fatalf("payloadOf() error = %v", err)
	}
	encoded, err := payload.MarshalMetadata()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("private-team-a")) {
		t.Fatal("payload retained an Account-protected Header value")
	}
	if !slices.Equal(redacted, []string{"X-Relay-Tenant"}) {
		t.Fatalf("payloadOf reported %v as redacted", redacted)
	}
}

func TestStoredEnvelopeNamesTheCredentialFieldsItRedacted(t *testing.T) {
	repository := &memoryRepository{}
	manager, err := Open(context.Background(), Options{
		Repository: repository,
		Random:     bytes.NewReader(bytes.Repeat([]byte{0x41}, 4096)),
		Clock:      fixedClock{value: time.Unix(1_790_000_000, 0).UTC()},
		Config:     redactionTestConfig(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = manager.Shutdown(context.Background()) }()

	observation := testObservation()
	observation.Headers = http.Header{
		"Authorization": []string{"Bearer private-token"},
		"X-Api-Key":     []string{"sk-do-not-store-me"},
		"Content-Type":  []string{"application/json"},
	}
	watermark, err := manager.Observe(context.Background(), observation)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Flush(context.Background(), watermark); err != nil {
		t.Fatal(err)
	}

	records := repository.snapshot()
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	want := []string{"Authorization", "X-Api-Key"}
	if !slices.Equal(records[0].RedactedCredentialFields, want) {
		t.Fatalf(
			"RedactedCredentialFields = %v, want %v",
			records[0].RedactedCredentialFields,
			want,
		)
	}
}

func TestRedactionKeepsFieldNameOrderAndMultiplicity(t *testing.T) {
	t.Parallel()

	headers := http.Header{}
	headers.Add("Set-Cookie", "a=1")
	headers.Add("Set-Cookie", "bb=22")
	headers.Set("Accept", "application/json")

	fields := canonicalHeaders(headers, testRedactor(t))

	if len(fields) != 2 ||
		fields[0].Name != "Accept" || fields[1].Name != "Set-Cookie" {
		t.Fatalf("canonicalHeaders() lost name or order: %+v", fields)
	}
	if len(fields[0].Values) != 1 || len(fields[0].Redacted) != 0 {
		t.Fatalf("a non-credential field was redacted: %+v", fields[0])
	}
	cookie := fields[1]
	if len(cookie.Values) != 0 || len(cookie.Redacted) != 2 {
		t.Fatalf("credential multiplicity was lost: %+v", cookie)
	}
	if cookie.Redacted[0].Bytes != 3 || cookie.Redacted[1].Bytes != 5 {
		t.Fatalf("redaction lost the observed value lengths: %+v", cookie)
	}
}

func TestRedactionDigestIdentifiesAValueOnlyInsideItsOwnDatabase(t *testing.T) {
	t.Parallel()

	here, err := NewRedactor(bytes.Repeat([]byte{0x01}, RedactionSaltBytes))
	if err != nil {
		t.Fatal(err)
	}
	elsewhere, err := NewRedactor(bytes.Repeat([]byte{0x02}, RedactionSaltBytes))
	if err != nil {
		t.Fatal(err)
	}
	digest := func(redactor Redactor, value string) string {
		return redactor.field("Authorization", []string{value}).Redacted[0].Digest
	}

	observed := digest(here, "Bearer one")
	observedAgain := digest(here, "Bearer one")
	rotated := digest(here, "Bearer two")
	foreign := digest(elsewhere, "Bearer one")

	if observed != observedAgain {
		t.Fatal("the same value did not produce a stable digest")
	}
	if observed == rotated {
		t.Fatal("a changed credential was not distinguishable")
	}
	if observed == foreign {
		t.Fatal("a redacted digest was portable between databases")
	}
}

func TestPayloadRefusesToBuildWithoutABoundRedactor(t *testing.T) {
	t.Parallel()

	headers := http.Header{}
	headers.Set("Authorization", "Bearer sk-ant-api03-do-not-store-me")

	_, _, err := payloadOf(Observation{Headers: headers}, nil, Redactor{})
	if err == nil {
		t.Fatal("payloadOf() built a payload without a bound redactor")
	}
}

// The predicate became destructive: before redaction it only set a flag, so an
// over-broad match cost nothing. Now an over-broad match destroys evidence, and
// both providers send rate-limit headers whose names contain "tokens".
func TestRateLimitEvidenceSurvivesRedaction(t *testing.T) {
	t.Parallel()

	redacted := map[string]bool{}
	for _, name := range []string{
		// Credentials: the value must not survive.
		"Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie",
		"X-Api-Key", "Api-Key", "X-Goog-Api-Key",
		// Measurements about tokens: the value is evidence and must survive.
		"Anthropic-Ratelimit-Input-Tokens-Limit",
		"Anthropic-Ratelimit-Output-Tokens-Remaining",
		"Anthropic-Ratelimit-Requests-Reset",
		"X-Ratelimit-Limit-Tokens",
		"X-Ratelimit-Remaining-Tokens",
		"X-Rate-Limit-Remaining-Tokens",
	} {
		redacted[name] = NameIsCredential(name)
	}

	for _, name := range []string{
		"Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie",
		"X-Api-Key", "Api-Key", "X-Goog-Api-Key",
	} {
		if !redacted[name] {
			t.Errorf("%s was not treated as a credential", name)
		}
	}
	for _, name := range []string{
		"Anthropic-Ratelimit-Input-Tokens-Limit",
		"Anthropic-Ratelimit-Output-Tokens-Remaining",
		"Anthropic-Ratelimit-Requests-Reset",
		"X-Ratelimit-Limit-Tokens",
		"X-Ratelimit-Remaining-Tokens",
		"X-Rate-Limit-Remaining-Tokens",
	} {
		if redacted[name] {
			t.Errorf("%s is a measurement, not a credential; its value was destroyed", name)
		}
	}
}

// An envelope that stored no payload redacted nothing. Reporting field names for
// it is a claim about work that never happened, in a product whose thesis is that
// evidence says what it is.
func TestAnUnstoredPayloadReportsNoRedaction(t *testing.T) {
	t.Parallel()

	repository := &memoryRepository{}
	manager, err := Open(context.Background(), Options{
		Repository: repository,
		Random:     bytes.NewReader(bytes.Repeat([]byte{0x41}, 4096)),
		Clock:      fixedClock{value: time.Unix(1_790_000_000, 0).UTC()},
		Config:     redactionTestConfig(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = manager.Shutdown(context.Background()) }()

	metadataOnly := testObservation()
	metadataOnly.Recording = RecordingMetadataOnly
	metadataOnly.ExchangeID = "exchange-metadata-only"
	unavailable := testObservation()
	unavailable.ExchangeID = "exchange-unavailable"
	unavailable.Unavailable = true
	unavailable.Complete = false
	unavailable.IncompleteReason = "response_stream_unavailable"
	unavailable.Body = nil

	var last Watermark
	for _, observation := range []Observation{metadataOnly, unavailable} {
		last, err = manager.Observe(context.Background(), observation)
		if err != nil {
			t.Fatalf("%s: %v", observation.ExchangeID, err)
		}
	}
	if err := manager.Flush(context.Background(), last); err != nil {
		t.Fatal(err)
	}

	for _, record := range repository.snapshot() {
		if len(record.RedactedCredentialFields) != 0 {
			t.Fatalf(
				"%s stored no payload yet reports %v as redacted",
				record.PayloadState, record.RedactedCredentialFields,
			)
		}
	}
}
