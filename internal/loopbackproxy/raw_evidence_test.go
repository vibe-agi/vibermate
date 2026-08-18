package loopbackproxy_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/vibe-agi/vibermate/internal/rawevidence"
)

func TestSemanticRequestRecordsClientIngressAndDownstreamRawEvidence(
	t *testing.T,
) {
	t.Parallel()

	observer := &rawObserver{}
	fixture := newProxyFixtureWithRawEvidence(t, observer)
	defer fixture.Close(t)

	secured := fixture.ConnectTLS(
		t,
		fixture.grant.ProxyCapability.Value(),
		"api.anthropic.com:443",
		"api.anthropic.com",
	)
	defer secured.Close()
	const requestBody = `{"model":"client"}`
	response := writeInnerRequest(t, secured, &http.Request{
		Method: http.MethodPost,
		URL:    mustURL(t, "/v1/messages?beta=true"),
		Host:   "api.anthropic.com:443",
		Header: http.Header{
			"Authorization": []string{"Bearer fixture-client-auth"},
			"Content-Type":  []string{"application/json"},
		},
		Body:          io.NopCloser(strings.NewReader(requestBody)),
		ContentLength: int64(len(requestBody)),
	})
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK ||
		string(responseBody) != `{"result":"proxied"}` {
		t.Fatalf("response status=%d body=%q", response.StatusCode, responseBody)
	}

	observations := observer.snapshot()
	if len(observations) != 2 {
		t.Fatalf("raw observations = %+v", observations)
	}
	ingress := observations[0]
	downstream := observations[1]
	if ingress.Layer != rawevidence.LayerClientIngress ||
		ingress.ScopeKind != rawevidence.ScopeManagedRun ||
		ingress.ScopeID != fixture.grant.Run.ID ||
		ingress.ExchangeID == "" || ingress.ConnectionID == "" ||
		ingress.AttemptID != "" || ingress.AccountID != "" ||
		ingress.RouteID != "route-proxy" ||
		ingress.UpstreamEndpointID == "" ||
		ingress.Method != http.MethodPost || ingress.Scheme != "https" ||
		ingress.Authority != "api.anthropic.com:443" ||
		ingress.Path != "/v1/messages" || ingress.RawQuery != "beta=true" ||
		string(ingress.Body) != requestBody || !ingress.Complete ||
		ingress.Headers.Get("Authorization") == "" {
		t.Fatalf("client ingress observation = %+v", ingress)
	}
	if downstream.Layer != rawevidence.LayerClientDownstream ||
		downstream.ExchangeID != ingress.ExchangeID ||
		downstream.ConnectionID != ingress.ConnectionID ||
		downstream.AttemptID != "" || downstream.AccountID != "" ||
		downstream.StatusCode != http.StatusOK ||
		downstream.Headers.Get("Content-Type") != "application/json" ||
		string(downstream.Body) != `{"result":"proxied"}` ||
		!downstream.Complete || downstream.TotalBodyBytes != int64(len(responseBody)) ||
		len(downstream.Frames) != 1 ||
		downstream.Frames[0].Kind != rawevidence.FrameData {
		t.Fatalf("client downstream observation = %+v", downstream)
	}
}

func TestSemanticRequestContinuesWhenIngressRawEvidenceFails(
	t *testing.T,
) {
	t.Parallel()

	observer := &rawObserver{failure: errors.New("fixture raw failure")}
	fixture := newProxyFixtureWithRawEvidence(t, observer)
	defer fixture.Close(t)

	secured := fixture.ConnectTLS(
		t,
		fixture.grant.ProxyCapability.Value(),
		"api.anthropic.com:443",
		"api.anthropic.com",
	)
	defer secured.Close()
	const requestBody = `{"model":"client"}`
	response := writeInnerRequest(t, secured, &http.Request{
		Method:        http.MethodPost,
		URL:           mustURL(t, "/v1/messages"),
		Host:          "api.anthropic.com:443",
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(strings.NewReader(requestBody)),
		ContentLength: int64(len(requestBody)),
	})
	responseBody, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK ||
		string(responseBody) != `{"result":"proxied"}` ||
		len(fixture.exchanges.Requests()) != 1 {
		t.Fatalf(
			"status=%d body=%q exchanges=%d",
			response.StatusCode,
			responseBody,
			len(fixture.exchanges.Requests()),
		)
	}
}

type rawObserver struct {
	mu           sync.Mutex
	observations []rawevidence.Observation
	failure      error
}

type rawScopeLease struct{}

func (rawScopeLease) Release() {}

type rawTerminalScope struct{}

func (rawTerminalScope) Commit() {}
func (rawTerminalScope) Abort()  {}

func (observer *rawObserver) BeginScope(
	_ context.Context,
	_ rawevidence.ScopeKind,
	_ string,
) (rawevidence.ScopeLease, error) {
	return rawScopeLease{}, nil
}

func (observer *rawObserver) PrepareTerminalScope(
	_ context.Context,
	_ rawevidence.ScopeKind,
	_ string,
) (rawevidence.TerminalScope, error) {
	return rawTerminalScope{}, nil
}

func (observer *rawObserver) Observe(
	_ context.Context,
	observation rawevidence.Observation,
) (rawevidence.Watermark, error) {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observation.Body = slices.Clone(observation.Body)
	observation.Headers = observation.Headers.Clone()
	observation.Frames = slices.Clone(observation.Frames)
	observer.observations = append(observer.observations, observation)
	if observer.failure != nil {
		return rawevidence.Watermark{}, observer.failure
	}
	return rawevidence.Watermark{WriterID: "fixture", Sequence: uint64(len(observer.observations))}, nil
}

func (observer *rawObserver) snapshot() []rawevidence.Observation {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return slices.Clone(observer.observations)
}
