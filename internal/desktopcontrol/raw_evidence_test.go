package desktopcontrol_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/rawevidence"
)

func TestRawEvidenceMetadataAndAuditedRevealContract(t *testing.T) {
	t.Parallel()
	body := []byte{0x00, 0xff, '\n', 'x'}
	digest := sha256.Sum256(body)
	reader := &rawEvidenceReaderFixture{
		statistics: rawevidence.Statistics{
			AdmittedRecords: 1, DurableWatermark: 1,
			MaximumUnflushedTime: 250 * time.Millisecond,
		},
		metadata: []rawevidence.EnvelopeMetadata{{
			EnvelopeID: "raw-envelope-1", Layer: rawevidence.LayerProviderResponse,
			ScopeKind: rawevidence.ScopeManagedRun, ScopeID: "run-1",
			ExchangeID: "exchange-1", AttemptID: "attempt-1",
			ObservedAt: time.Date(2026, 8, 13, 1, 2, 3, 0, time.UTC),
			ExpiresAt:  time.Date(2026, 9, 12, 1, 2, 3, 0, time.UTC),
			StatusCode: 200, Authority: "api.anthropic.com", Path: "/v1/messages",
			HeaderCount: 2, BodyBytes: int64(len(body)), BodySHA256: digest,
			DigestScope: rawevidence.DigestFull, PayloadState: rawevidence.PayloadCaptured,
			ContainsSecret: true,
		}},
		payload: rawevidence.Payload{
			Version: 1,
			Headers: []rawevidence.HeaderField{{Name: "Authorization", Values: []string{"Bearer private"}}},
			Body:    body,
			Frames:  []rawevidence.Frame{{Kind: rawevidence.FrameData, Offset: 0, Length: int64(len(body))}},
		},
	}
	fixture := newAuditFixtureWithRawEvidence(t, reader)

	listed := doRequest(t, fixture.router, fixture.authority, http.MethodGet,
		"/api/v1/exchanges/exchange-1/raw-evidence", fixture.readToken, nil)
	if listed.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body)
	}
	for _, forbidden := range []string{"cipherNonce", "ciphertext", "Bearer private", "bodyBase64"} {
		if strings.Contains(listed.Body.String(), forbidden) {
			t.Fatalf("metadata response leaked %q: %s", forbidden, listed.Body)
		}
	}
	var metadata struct {
		Items []struct {
			EnvelopeID      string `json:"envelopeId"`
			BodySHA256      string `json:"bodySha256"`
			RevealAvailable bool   `json:"revealAvailable"`
		} `json:"items"`
		Writer struct {
			State            string `json:"state"`
			AdmittedRecords  uint64 `json:"admittedRecords"`
			DurableWatermark uint64 `json:"durableWatermark"`
		} `json:"writer"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &metadata); err != nil {
		t.Fatal(err)
	}
	if len(metadata.Items) != 1 || metadata.Items[0].EnvelopeID != "raw-envelope-1" ||
		!metadata.Items[0].RevealAvailable || metadata.Items[0].BodySHA256 == "" {
		t.Fatalf("metadata=%+v", metadata)
	}
	if metadata.Writer.State != "active" || metadata.Writer.AdmittedRecords != 1 ||
		metadata.Writer.DurableWatermark != 1 {
		t.Fatalf("writer=%+v", metadata.Writer)
	}

	readOnly := rawRevealRequest(t, fixture, fixture.readToken, nil)
	if readOnly.Code != http.StatusUnauthorized || len(reader.requests) != 0 {
		t.Fatalf("read token reveal status=%d requests=%+v", readOnly.Code, reader.requests)
	}
	invalid := rawRevealRequest(t, fixture, fixture.writeToken,
		[]byte(`{"unexpected":"workflow field"}`))
	if invalid.Code != http.StatusUnprocessableEntity || len(reader.requests) != 0 {
		t.Fatalf("unexpected reveal body status=%d requests=%+v", invalid.Code, reader.requests)
	}

	revealed := rawRevealRequest(t, fixture, fixture.writeToken, nil)
	if revealed.Code != http.StatusOK || revealed.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("reveal status=%d headers=%v body=%s", revealed.Code, revealed.Header(), revealed.Body)
	}
	var response struct {
		BodyBase64 string                    `json:"bodyBase64"`
		Headers    []rawevidence.HeaderField `json:"headers"`
	}
	if err := json.Unmarshal(revealed.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(response.BodyBase64)
	if err != nil || string(decoded) != string(body) || len(response.Headers) != 1 {
		t.Fatalf("response=%+v decoded=%v err=%v", response, decoded, err)
	}
	if len(reader.requests) != 1 ||
		!strings.HasPrefix(reader.requests[0].ActorID, "desktop-app:") ||
		reader.requests[0].ActorID == "desktop-app:" {
		t.Fatalf("audited reveal request=%+v", reader.requests)
	}
}

func TestRawEvidenceRevealEncodesEmptyCollectionsAsArrays(t *testing.T) {
	t.Parallel()
	emptyDigest := sha256.Sum256(nil)
	reader := &rawEvidenceReaderFixture{
		metadata: []rawevidence.EnvelopeMetadata{{
			EnvelopeID: "raw-envelope-1", Layer: rawevidence.LayerProviderResponse,
			ScopeKind: rawevidence.ScopeManagedRun, ScopeID: "run-1",
			ExchangeID: "exchange-1", AttemptID: "attempt-1",
			ObservedAt: time.Date(2026, 8, 13, 1, 2, 3, 0, time.UTC),
			ExpiresAt:  time.Date(2026, 9, 12, 1, 2, 3, 0, time.UTC),
			StatusCode: 204, Authority: "api.anthropic.com", Path: "/v1/messages",
			BodySHA256: emptyDigest, DigestScope: rawevidence.DigestFull,
			PayloadState: rawevidence.PayloadCaptured,
		}},
		payload: rawevidence.Payload{Version: 1},
	}
	fixture := newAuditFixtureWithRawEvidence(t, reader)

	revealed := rawRevealRequest(t, fixture, fixture.writeToken, nil)
	if revealed.Code != http.StatusOK {
		t.Fatalf("reveal status=%d body=%s", revealed.Code, revealed.Body)
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(revealed.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"headers", "trailers", "frames"} {
		if got := string(response[field]); got != "[]" {
			t.Fatalf("%s must be an empty JSON array, got %s", field, got)
		}
	}
}

func rawRevealRequest(
	t *testing.T,
	fixture *auditFixture,
	token string,
	body []byte,
) *httptest.ResponseRecorder {
	t.Helper()
	request := newRequest(http.MethodPost, fixture.authority,
		"/api/v1/raw-evidence/raw-envelope-1/actions/reveal", token, body)
	if len(body) != 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	fixture.router.ServeHTTP(recorder, request)
	return recorder
}

type rawEvidenceReaderFixture struct {
	metadata   []rawevidence.EnvelopeMetadata
	payload    rawevidence.Payload
	requests   []rawevidence.RevealRequest
	statistics rawevidence.Statistics
}

func (reader *rawEvidenceReaderFixture) ListExchange(
	_ context.Context,
	exchangeID string,
) ([]rawevidence.EnvelopeMetadata, error) {
	if exchangeID == "" {
		return nil, rawevidence.ErrInvalidRead
	}
	return append([]rawevidence.EnvelopeMetadata(nil), reader.metadata...), nil
}

func (reader *rawEvidenceReaderFixture) Reveal(
	_ context.Context,
	request rawevidence.RevealRequest,
) (rawevidence.RevealedEnvelope, error) {
	if err := request.Validate(); err != nil {
		return rawevidence.RevealedEnvelope{}, err
	}
	if len(reader.metadata) == 0 || request.EnvelopeID != reader.metadata[0].EnvelopeID {
		return rawevidence.RevealedEnvelope{}, rawevidence.ErrEnvelopeNotFound
	}
	reader.requests = append(reader.requests, request)
	return rawevidence.RevealedEnvelope{
		Metadata: reader.metadata[0], Payload: reader.payload,
	}, nil
}

func (*rawEvidenceReaderFixture) Recovery() rawevidence.Recovery { return rawevidence.Recovery{} }

func (reader *rawEvidenceReaderFixture) Statistics() rawevidence.Statistics {
	return reader.statistics
}

var _ rawevidence.Reader = (*rawEvidenceReaderFixture)(nil)
