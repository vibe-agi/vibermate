package modelcatalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestModelsDevCachesCanonicalMetadataWithoutInterpretingEndpointModelIDs(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.URL.Path != "/models.json" {
			t.Fatalf("unexpected models.dev path %q", request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
			"deepseek/deepseek-v4-flash-0731": {
				"id": "deepseek/deepseek-v4-flash-0731",
				"name": "DeepSeek V4 Flash 0731",
				"description": "Fast open-weight reasoning model",
				"family": "deepseek-flash",
				"attachment": false,
				"reasoning": true,
				"tool_call": true,
				"structured_output": true,
				"knowledge": "2025-05",
				"release_date": "2026-04-24",
				"modalities": {"input":["text"],"output":["text"]},
				"open_weights": true,
				"limit": {"context":1000000,"output":384000}
			},
			"anthropic/claude-opus-4-1": {
				"id": "anthropic/claude-opus-4-1",
				"name": "Claude Opus 4.1",
				"family": "claude-opus",
				"attachment": true,
				"reasoning": true,
				"tool_call": true,
				"modalities": {"input":["text","image"],"output":["text"]},
				"limit": {"context":200000,"output":32000}
			}
		}`))
	}))
	t.Cleanup(server.Close)

	directory, err := NewModelsDev(ModelsDevOptions{
		Transport: modelsDevTransportClient{client: server.Client(), url: server.URL + "/models.json"},
		Clock:     fixedClock{now: time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("new models.dev directory: %v", err)
	}

	for _, endpointModelID := range []string{
		"dashscope:deepseek-v4-flash-0731",
		"dashscope_deepseek-v4-flash-0731",
		"deepseek-v4-flash-0731",
	} {
		metadata, found, err := directory.Lookup(context.Background(), endpointModelID)
		if err != nil || found {
			t.Fatalf("opaque Endpoint model ID %q must not be interpreted: metadata=%#v found=%t err=%v", endpointModelID, metadata, found, err)
		}
	}
	metadata, found, err := directory.Lookup(context.Background(), "deepseek/deepseek-v4-flash-0731")
	if err != nil || !found {
		t.Fatalf("lookup exact metadata id: found=%t err=%v", found, err)
	}
	if metadata.CanonicalID != "deepseek/deepseek-v4-flash-0731" ||
		metadata.DisplayName != "DeepSeek V4 Flash 0731" || !metadata.OpenWeights ||
		metadata.ContextLimit != 1_000_000 || metadata.OutputLimit != 384_000 {
		t.Fatalf("unexpected metadata: %#v", metadata)
	}
	anthropic, err := directory.ListProvider(context.Background(), "anthropic")
	if err != nil || len(anthropic) != 1 || anthropic[0].CanonicalID != "anthropic/claude-opus-4-1" {
		t.Fatalf("unexpected provider models: %#v err=%v", anthropic, err)
	}
	if requests.Load() != 1 {
		t.Fatalf("metadata should be fetched once, got %d requests", requests.Load())
	}
}

func TestModelsDevOnlyResolvesExactMetadataIDs(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
			"vendor-a/shared-model":{"id":"vendor-a/shared-model","name":"Vendor A Model"},
			"vendor-b/shared-model":{"id":"vendor-b/shared-model","name":"Vendor B Model"}
		}`))
	}))
	t.Cleanup(server.Close)

	directory, err := NewModelsDev(ModelsDevOptions{
		Transport: modelsDevTransportClient{client: server.Client(), url: server.URL},
		Clock:     fixedClock{now: time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("new models.dev directory: %v", err)
	}
	for _, endpointModelID := range []string{"shared-model", "relay:shared-model", "relay_shared-model"} {
		if metadata, found, err := directory.Lookup(context.Background(), endpointModelID); err != nil || found {
			t.Fatalf("non-exact ID %q must not resolve: metadata=%#v found=%t err=%v", endpointModelID, metadata, found, err)
		}
	}
	if metadata, found, err := directory.Lookup(context.Background(), "vendor-a/shared-model"); err != nil || !found || metadata.DisplayName != "Vendor A Model" {
		t.Fatalf("canonical id should resolve: metadata=%#v found=%t err=%v", metadata, found, err)
	}
}

type modelsDevTransportClient struct {
	client *http.Client
	url    string
}

func (transport modelsDevTransportClient) FetchModelsDev(ctx context.Context) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, transport.url, nil)
	if err != nil {
		return nil, err
	}
	return transport.client.Do(request)
}
