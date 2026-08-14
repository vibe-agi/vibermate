package providertransport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/vibe-agi/vibermate/internal/rawevidence"
)

func TestProviderRawEvidenceCapturesAuthenticatedEgressAndBoundedResponse(
	t *testing.T,
) {
	t.Parallel()

	const responsePayload = "provider-response"
	observer := &rawObserverStub{}
	gate := newStartedGate(t)
	authenticator, err := NewStaticBearerAuthenticator(
		&secretReaderStub{value: []byte("provider-token")},
	)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(ClientOptions{
		Coordinator:   gate,
		Authenticator: authenticator,
		RawEvidence:   observer,
		// The response must still reach the caller in full while Raw retention
		// keeps only a bounded prefix.
		RawResponseBodyBytes: 4,
		Transport: &roundTripperStub{response: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"application/json"},
				"X-Repeated":   []string{"first", "second"},
			},
			Body: io.NopCloser(strings.NewReader(responsePayload)),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownClient(t, client)

	requestBody := []byte(`{"model":"provider-model"}`)
	request := withRawEvidence(
		t,
		newTestRequestWithBody(
			t,
			gate,
			"raw-success",
			testTarget("provider.example", 443),
			http.Header{"X-Client": []string{"kept"}},
			requestBody,
		),
		rawevidence.RecordingFull,
	)
	response, _, err := client.Do(context.Background(), request)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	gotBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatalf("response Close() error = %v", err)
	}
	if string(gotBody) != responsePayload {
		t.Fatalf("downstream body = %q", gotBody)
	}

	observations := observer.snapshot()
	if len(observations) != 2 {
		t.Fatalf("raw observations = %d, want 2", len(observations))
	}
	egress := observations[0]
	if egress.Layer != rawevidence.LayerProviderEgress ||
		egress.Headers.Get("Authorization") != "Bearer provider-token" ||
		egress.Headers.Get("X-Client") != "kept" ||
		!bytes.Equal(egress.Body, requestBody) || !egress.Complete {
		t.Fatalf("provider egress observation = %+v", egress)
	}
	providerResponse := observations[1]
	wantDigest := sha256.Sum256([]byte(responsePayload))
	if providerResponse.Layer != rawevidence.LayerProviderResponse ||
		providerResponse.StatusCode != http.StatusOK ||
		string(providerResponse.Body) != responsePayload[:4] ||
		providerResponse.TotalBodyBytes != int64(len(responsePayload)) ||
		providerResponse.BodySHA256 != wantDigest ||
		!providerResponse.DigestAvailable ||
		!providerResponse.FullDigestAvailable ||
		providerResponse.Complete ||
		providerResponse.IncompleteReason != "response_payload_limit" ||
		!slices.Equal(
			providerResponse.Headers.Values("X-Repeated"),
			[]string{"first", "second"},
		) {
		t.Fatalf("provider response observation = %+v", providerResponse)
	}
}

func TestProviderRawEvidenceRecordsUnavailableTransportResponse(t *testing.T) {
	t.Parallel()

	observer := &rawObserverStub{}
	gate := newStartedGate(t)
	authenticator, err := NewStaticBearerAuthenticator(
		&secretReaderStub{value: []byte("provider-token")},
	)
	if err != nil {
		t.Fatal(err)
	}
	transportFailure := errors.New("provider dial failed")
	client, err := NewClient(ClientOptions{
		Coordinator:   gate,
		Authenticator: authenticator,
		RawEvidence:   observer,
		Transport:     &roundTripperStub{err: transportFailure},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownClient(t, client)

	request := withRawEvidence(
		t,
		newTestRequest(
			t,
			gate,
			"raw-transport-failure",
			testTarget("provider.example", 443),
			nil,
		),
		rawevidence.RecordingFull,
	)
	response, _, err := client.Do(context.Background(), request)
	if !errors.Is(err, transportFailure) || response != nil {
		t.Fatalf("Do() response=%v error=%v", response, err)
	}
	observations := observer.snapshot()
	if len(observations) != 2 {
		t.Fatalf("raw observations = %d, want 2", len(observations))
	}
	unavailable := observations[1]
	if unavailable.Layer != rawevidence.LayerProviderResponse ||
		!unavailable.Unavailable || unavailable.Complete ||
		unavailable.IncompleteReason != "transport_failed" ||
		len(unavailable.Body) != 0 || unavailable.DigestAvailable {
		t.Fatalf("unavailable response observation = %+v", unavailable)
	}
}

func TestProviderRawEvidenceRecordingOffWritesNothing(t *testing.T) {
	t.Parallel()

	observer := &rawObserverStub{}
	gate := newStartedGate(t)
	authenticator, err := NewStaticBearerAuthenticator(
		&secretReaderStub{value: []byte("provider-token")},
	)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(ClientOptions{
		Coordinator:   gate,
		Authenticator: authenticator,
		RawEvidence:   observer,
		Transport: &roundTripperStub{response: &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("response")),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownClient(t, client)

	request := withRawEvidence(
		t,
		newTestRequest(
			t,
			gate,
			"raw-recording-off",
			testTarget("provider.example", 443),
			nil,
		),
		rawevidence.RecordingOff,
	)
	response, _, err := client.Do(context.Background(), request)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatalf("response Close() error = %v", err)
	}
	if observations := observer.snapshot(); len(observations) != 0 {
		t.Fatalf("disabled Raw recording wrote %d observations", len(observations))
	}
}

func TestProviderRawEvidenceFailureNeverChangesProviderResponse(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		failAfter int
	}{
		{name: "provider egress", failAfter: 1},
		{name: "provider response", failAfter: 2},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			observer := &rawObserverStub{
				failure:   errors.New("fixture Raw writer failure"),
				failAfter: test.failAfter,
			}
			gate := newStartedGate(t)
			authenticator, err := NewStaticBearerAuthenticator(
				&secretReaderStub{value: []byte("provider-token")},
			)
			if err != nil {
				t.Fatal(err)
			}
			transport := &roundTripperStub{response: &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("provider-response")),
			}}
			client, err := NewClient(ClientOptions{
				Coordinator: gate, Authenticator: authenticator,
				RawEvidence: observer, Transport: transport,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer shutdownClient(t, client)

			request := withRawEvidence(
				t,
				newTestRequest(
					t, gate, "raw-fail-open", testTarget("provider.example", 443), nil,
				),
				rawevidence.RecordingFull,
			)
			response, _, err := client.Do(context.Background(), request)
			if err != nil {
				t.Fatalf("Do() error = %v", err)
			}
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatalf("ReadAll() error = %v", err)
			}
			if err := response.Body.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
			if string(body) != "provider-response" || transport.callCount() != 1 {
				t.Fatalf("body=%q transport calls=%d", body, transport.callCount())
			}
		})
	}
}

func withRawEvidence(
	t *testing.T,
	request Request,
	mode rawevidence.RecordingMode,
) Request {
	t.Helper()
	retentionDays := uint16(30)
	if mode == rawevidence.RecordingOff {
		retentionDays = 0
	}
	rawContext := rawevidence.Context{
		ScopeKind:           rawevidence.ScopeManagedRun,
		ScopeID:             "capture-raw-test",
		ExchangeID:          request.exchangeID,
		ConnectionID:        request.connectionID,
		AttemptID:           request.egressAttemptID,
		EnvironmentID:       request.provenance.environmentID.String(),
		EnvironmentRevision: uint64(request.provenance.environmentRevision),
		EnvironmentDigest:   request.provenance.environmentDigest.String(),
		RouteID:             request.provenance.routeID.String(),
		RouteRevision:       uint64(request.provenance.routeRevision),
		AccountID:           request.accountRef.ID,
		AccountRevision:     request.accountRef.Revision,
		CredentialEpoch:     request.accountRef.CredentialEpoch,
		Recording:           mode,
		RetentionDays:       retentionDays,
	}
	if err := rawContext.Validate(); err != nil {
		t.Fatalf("raw context Validate() error = %v", err)
	}
	request.rawEvidence = &rawContext
	return request
}

type rawObserverStub struct {
	mu           sync.Mutex
	observations []rawevidence.Observation
	failure      error
	failAfter    int
}

func (observer *rawObserverStub) Observe(
	_ context.Context,
	observation rawevidence.Observation,
) (rawevidence.Watermark, error) {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observation.Headers = observation.Headers.Clone()
	observation.Trailers = observation.Trailers.Clone()
	observation.Body = bytes.Clone(observation.Body)
	observation.Frames = slices.Clone(observation.Frames)
	observer.observations = append(observer.observations, observation)
	if observer.failure != nil &&
		(observer.failAfter == 0 || len(observer.observations) >= observer.failAfter) {
		return rawevidence.Watermark{}, observer.failure
	}
	return rawevidence.Watermark{
		WriterID: "raw-observer-test",
		Sequence: uint64(len(observer.observations)),
	}, nil
}

func (observer *rawObserverStub) snapshot() []rawevidence.Observation {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return slices.Clone(observer.observations)
}
