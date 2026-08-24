package modelcatalog

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

const testCatalogAccountID provideraccount.ID = "account.spark"

func TestDiscoverUsesOnlyEndpointAsAvailabilityAuthority(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/v1/models" {
			t.Fatalf("unexpected model catalog request %s %s", request.Method, request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
			"object":"list",
			"data":[{
				"id":"dashscope:deepseek-v4-flash-0731",
				"object":"model",
				"owned_by":"vllm",
				"max_model_len":1048576
			}]
		}`))
	}))
	t.Cleanup(server.Close)

	origin, err := originidentity.ParseProviderOrigin(server.URL)
	if err != nil {
		t.Fatalf("parse test endpoint origin: %v", err)
	}
	endpoint := testEndpoint(origin)
	catalog, err := New(Options{
		Endpoints:   endpointReaderStub{endpoint: endpoint},
		Credentials: credentialAuthorityStub{},
		Transport:   endpointTransportClient{client: server.Client()},
		Clock:       fixedClock{now: time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("new model catalog: %v", err)
	}

	snapshot, err := catalog.Discover(
		context.Background(), endpoint.ID, testCatalogAccountID, false,
	)
	if err != nil {
		t.Fatalf("discover endpoint models: %v", err)
	}
	if snapshot.EndpointID != endpoint.ID || snapshot.EndpointRevision != endpoint.Revision {
		t.Fatalf("unexpected endpoint evidence: %#v", snapshot)
	}
	if snapshot.AccountID != testCatalogAccountID || snapshot.AccountRevision != 5 ||
		snapshot.CredentialEpoch != 7 {
		t.Fatalf("unexpected Account evidence: %#v", snapshot)
	}
	if snapshot.AvailabilitySource != AvailabilitySourceEndpoint {
		t.Fatalf("unexpected sources: %#v", snapshot)
	}
	if len(snapshot.Models) != 1 {
		t.Fatalf("expected one live model, got %#v", snapshot.Models)
	}
	model := snapshot.Models[0]
	if model.ID != "dashscope:deepseek-v4-flash-0731" || !model.VerifiedAvailable ||
		model.DisplayName != "" || model.OwnedBy != "vllm" ||
		model.ContextLimit != 1_048_576 || model.OutputLimit != 0 {
		t.Fatalf("unexpected Endpoint-owned model: %#v", model)
	}
}

func TestDiscoverDefaultBudgetIncludesHostCredentialAccess(t *testing.T) {
	t.Parallel()

	origin, err := originidentity.ParseProviderOrigin("https://relay.example.test")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := testEndpoint(origin)
	observedBudget := make(chan time.Duration, 1)
	catalog, err := New(Options{
		Endpoints:   endpointReaderStub{endpoint: endpoint},
		Credentials: credentialAuthorityStub{},
		Transport: endpointTransportFunc(func(ctx context.Context, _ upstreamendpoint.Endpoint) (*http.Response, error) {
			deadline, ok := ctx.Deadline()
			if !ok {
				observedBudget <- 0
			} else {
				observedBudget <- time.Until(deadline)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"data":[{"id":"opaque-model"}]}`)),
			}, nil
		}),
		Clock: fixedClock{now: time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Discover(
		context.Background(), endpoint.ID, testCatalogAccountID, true,
	); err != nil {
		t.Fatal(err)
	}
	if budget := <-observedBudget; budget < 19*time.Second {
		t.Fatalf("default discovery budget = %s, want at least 19s", budget)
	}
}

func TestDiscoverPreservesTransportTimeoutCause(t *testing.T) {
	t.Parallel()

	origin, err := originidentity.ParseProviderOrigin("https://relay.example.test")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := testEndpoint(origin)
	catalog, err := New(Options{
		Endpoints:   endpointReaderStub{endpoint: endpoint},
		Credentials: credentialAuthorityStub{},
		Transport: endpointTransportFunc(func(context.Context, upstreamendpoint.Endpoint) (*http.Response, error) {
			return nil, context.DeadlineExceeded
		}),
		Clock: fixedClock{now: time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = catalog.Discover(
		context.Background(), endpoint.ID, testCatalogAccountID, true,
	)
	if !errors.Is(err, ErrCatalogUnavailable) ||
		!errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("discovery error did not preserve its timeout cause: %v", err)
	}
}

func TestDiscoverDoesNotGuessAvailabilityForCustomEndpoint(t *testing.T) {
	t.Parallel()

	origin, err := originidentity.ParseProviderOrigin("https://relay.example.test")
	if err != nil {
		t.Fatalf("parse custom endpoint origin: %v", err)
	}
	endpoint := testEndpoint(origin)
	catalog, err := New(Options{
		Endpoints:   endpointReaderStub{endpoint: endpoint},
		Credentials: credentialAuthorityStub{},
		Transport: endpointTransportFunc(func(context.Context, upstreamendpoint.Endpoint) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusUnauthorized,
				Body:       io.NopCloser(strings.NewReader(`{"error":"unauthorized"}`)),
			}, nil
		}),
		Clock: fixedClock{now: time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("new model catalog: %v", err)
	}

	_, err = catalog.Discover(
		context.Background(), endpoint.ID, testCatalogAccountID, false,
	)
	if !errors.Is(err, ErrCatalogUnavailable) {
		t.Fatalf("custom endpoint must not inherit directory availability, got %v", err)
	}
}

func TestDiscoverNeverTreatsMetadataAsEndpointAvailability(t *testing.T) {
	t.Parallel()

	origin, err := originidentity.ParseProviderOrigin("https://api.anthropic.com")
	if err != nil {
		t.Fatalf("parse official endpoint origin: %v", err)
	}
	endpoint := testEndpoint(origin)
	catalog, err := New(Options{
		Endpoints:   endpointReaderStub{endpoint: endpoint},
		Credentials: credentialAuthorityStub{},
		Transport: endpointTransportFunc(func(context.Context, upstreamendpoint.Endpoint) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusUnauthorized,
				Body:       io.NopCloser(strings.NewReader(`{"error":"unauthorized"}`)),
			}, nil
		}),
		Clock: fixedClock{now: time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("new model catalog: %v", err)
	}

	_, err = catalog.Discover(
		context.Background(), endpoint.ID, testCatalogAccountID, false,
	)
	if !errors.Is(err, ErrCatalogUnavailable) {
		t.Fatalf("Endpoint failure must remain unavailable, got %v", err)
	}
}

func TestDiscoverEndpointRejectsInvalidUTF8BeforeJSONReplacement(t *testing.T) {
	t.Parallel()

	body := append([]byte(`{"data":[{"id":"opaque-`), 0xff)
	body = append(body, []byte(`"}]}`)...)
	catalog := &Service{transport: endpointTransportFunc(func(
		context.Context,
		upstreamendpoint.Endpoint,
	) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(string(body))),
		}, nil
	})}

	_, err := catalog.discoverEndpoint(
		context.Background(),
		upstreamendpoint.Endpoint{},
		nil,
	)
	if !errors.Is(err, ErrInvalidCatalog) {
		t.Fatalf("invalid UTF-8 must be rejected before JSON replacement, got %v", err)
	}
}

type endpointReaderStub struct {
	endpoint upstreamendpoint.Endpoint
	err      error
}

type credentialAuthorityStub struct{}

func (credentialAuthorityStub) AcquireEndpointCredential(
	_ context.Context,
	accountID provideraccount.ID,
	endpoint upstreamendpoint.Endpoint,
) (providerauth.Lease, error) {
	return &catalogCredentialLease{account: providerauth.AccountRef{
		ID:              accountID.String(),
		Revision:        5,
		CredentialEpoch: 7,
		RealmID:         endpoint.RealmID,
	}}, nil
}

type catalogCredentialLease struct {
	account providerauth.AccountRef
}

func (*catalogCredentialLease) Mode() providerauth.CredentialMode {
	return providerauth.CredentialManaged
}

func (*catalogCredentialLease) Driver() providerauth.DriverRef {
	return providerauth.StaticHeaderDriverRef()
}

func (*catalogCredentialLease) Secret() secretstore.Reference { return secretstore.Reference{} }

func (lease *catalogCredentialLease) Account() (providerauth.AccountRef, bool) {
	return lease.account, true
}

func (*catalogCredentialLease) Release() {}

func (stub endpointReaderStub) Get(context.Context, upstreamendpoint.ID) (upstreamendpoint.Endpoint, error) {
	return stub.endpoint.Clone(), stub.err
}

type fixedClock struct{ now time.Time }

func (clock fixedClock) Now() time.Time { return clock.now }

type endpointTransportFunc func(context.Context, upstreamendpoint.Endpoint) (*http.Response, error)

func (function endpointTransportFunc) FetchEndpointModels(
	ctx context.Context,
	endpoint upstreamendpoint.Endpoint,
	_ providerauth.Lease,
) (*http.Response, error) {
	return function(ctx, endpoint)
}

type endpointTransportClient struct{ client *http.Client }

func (transport endpointTransportClient) FetchEndpointModels(
	ctx context.Context,
	endpoint upstreamendpoint.Endpoint,
	_ providerauth.Lease,
) (*http.Response, error) {
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		strings.TrimSuffix(endpoint.Origin.String(), "/")+"/v1/models",
		nil,
	)
	if err != nil {
		return nil, err
	}
	return transport.client.Do(request)
}

func testEndpoint(origin originidentity.ProviderOrigin) upstreamendpoint.Endpoint {
	now := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	return upstreamendpoint.Endpoint{
		ID:               "endpoint.spark",
		DisplayName:      "DGX Spark",
		Origin:           origin,
		RealmID:          "openai.platform",
		BackendProtocols: []string{"openai_responses", "openai_chat"},
		Capabilities: []protocolspec.ProviderCapability{
			protocolspec.ProviderCapabilityMessages,
			protocolspec.ProviderCapabilityStreaming,
			protocolspec.ProviderCapabilityToolCalls,
		},
		Drivers:   []providerauth.DriverRef{providerauth.StaticHeaderDriverRef()},
		State:     upstreamendpoint.StateActive,
		Revision:  3,
		CreatedAt: now,
		UpdatedAt: now,
	}
}
