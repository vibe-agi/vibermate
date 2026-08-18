package rawevidence

import (
	"crypto/sha256"
	"strings"
	"testing"
	"time"
)

func TestStoredEnvelopeValidateMatchesSQLiteBounds(t *testing.T) {
	t.Parallel()
	valid := validStoredEnvelopeForValidation()
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid envelope = %v", err)
	}

	tests := map[string]func(*StoredEnvelope){
		"watermark overflow": func(value *StoredEnvelope) {
			value.Watermark = maxSQLiteInteger + 1
		},
		"authority identity overflow": func(value *StoredEnvelope) {
			value.AttemptID = strings.Repeat("a", MaxIdentityBytes+1)
		},
		"revision overflow": func(value *StoredEnvelope) {
			value.RouteRevision = maxSQLiteInteger + 1
		},
		"environment digest overflow": func(value *StoredEnvelope) {
			value.EnvironmentDigest = strings.Repeat(
				"d",
				maxEnvironmentDigestBytes+1,
			)
		},
		"method overflow": func(value *StoredEnvelope) {
			value.Method = strings.Repeat("M", maxHTTPMethodBytes+1)
		},
		"scheme overflow": func(value *StoredEnvelope) {
			value.Scheme = strings.Repeat("s", maxHTTPSchemeBytes+1)
		},
		"metadata overflow": func(value *StoredEnvelope) {
			value.Path = strings.Repeat("p", maxHTTPMetadataBytes+1)
		},
		"canonicalization mismatch": func(value *StoredEnvelope) {
			value.Canonicalization = "packet_capture"
		},
		"sub-millisecond expiry": func(value *StoredEnvelope) {
			value.ExpiresAt = value.ObservedAt.Add(time.Microsecond)
		},
	}
	for name, mutate := range tests {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			value := valid
			mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("invalid envelope passed validation")
			}
		})
	}
}

func TestObservationRejectsPersistencePoisonBeforeQueueAdmission(t *testing.T) {
	t.Parallel()
	value := testObservation()
	value.Scheme = strings.Repeat("s", maxHTTPSchemeBytes+1)
	if err := value.validate(); err == nil {
		t.Fatal("oversized scheme passed observation validation")
	}

	value = testObservation()
	value.EnvironmentRevision = maxSQLiteInteger + 1
	if err := value.validate(); err == nil {
		t.Fatal("overflowing revision passed observation validation")
	}
}

func validStoredEnvelopeForValidation() StoredEnvelope {
	observedAt := time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)
	return StoredEnvelope{
		EnvelopeID:       "writer-1.1",
		WriterID:         "writer-1",
		Watermark:        1,
		Layer:            LayerClientIngress,
		ScopeKind:        ScopeManagedRun,
		ScopeID:          "capture-1",
		ExchangeID:       "exchange-1",
		ObservedAt:       observedAt,
		ExpiresAt:        observedAt.Add(24 * time.Hour),
		Method:           "POST",
		Scheme:           "https",
		Authority:        "api.anthropic.com",
		Path:             "/v1/messages",
		Representation:   "http_message",
		Canonicalization: httpCanonicalization,
		BodySHA256:       sha256.Sum256(nil),
		DigestScope:      DigestFull,
		PayloadState:     PayloadMetadataOnly,
		PayloadReason:    "recording_metadata_only",
	}
}

// A captured observation retained the whole message, so the stored body must be
// the whole message. Without this, an envelope can claim body_bytes while storing
// nothing and a read returns an empty body with a success outcome — the partial
// result the store must never produce.
func TestCapturedEnvelopeMustStoreEveryByteItCounted(t *testing.T) {
	t.Parallel()

	valid := validStoredEnvelopeForValidation()
	valid.PayloadState = PayloadCaptured
	valid.PayloadMetadata = []byte(`{"version":1,"headers":[]}`)
	valid.Body = []byte("the stored bytes")
	valid.BodyBytes = int64(len(valid.Body))
	valid.BodySHA256 = sha256.Sum256(valid.Body)
	valid.DigestScope = DigestFull
	if err := valid.Validate(); err != nil {
		t.Fatalf("a consistent captured envelope was rejected: %v", err)
	}

	shortStore := valid
	shortStore.BodyBytes = 5000
	if err := shortStore.Validate(); err == nil {
		t.Fatal("a captured envelope counting more bytes than it stored was accepted")
	}

	truncated := valid
	truncated.PayloadState = PayloadTruncated
	truncated.PayloadReason = "response_body_limit"
	truncated.BodyBytes = 5000
	if err := truncated.Validate(); err != nil {
		t.Fatalf("a truncated envelope retaining a prefix was rejected: %v", err)
	}

	overStore := truncated
	overStore.BodyBytes = 2
	if err := overStore.Validate(); err == nil {
		t.Fatal("an envelope storing more bytes than it observed was accepted")
	}

	// body_sha256 is observation evidence recorded independently of the stored
	// bytes. Verifying reassembly against the body row's own key proves the chunks
	// join correctly; only this comparison proves they are the bytes the envelope
	// says were observed.
	wrongDigest := valid
	wrongDigest.BodySHA256 = sha256.Sum256([]byte("a different body"))
	if err := wrongDigest.Validate(); err == nil {
		t.Fatal("a captured body disagreeing with its recorded digest was accepted")
	}
}
