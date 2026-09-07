package loopbackproxy_test

import (
	"bytes"
	"io"
	"net/http"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
)

func TestCodexZstdIngressKeepsWireEvidenceAndExactOperation(t *testing.T) {
	for _, test := range []struct{ host, path, operation string }{
		{"chatgpt.com", "/backend-api/codex/responses", "openai-codex-responses-create"},
		{"api.openai.com", "/v1/responses", "openai-responses-create"},
	} {
		t.Run(test.host, func(t *testing.T) {
			t.Parallel()
			observer := &rawObserver{}
			adapter := fixedCodexAdapterEvidence()
			fixture := newProxyFixtureForDialectWithPolicyAndRawEvidence(t,
				protocolspec.DialectOpenAIResponses, &adapter, allowEverythingTestPolicy(t),
				observer, environment.SystemTransparentID)
			defer fixture.Close(t)
			secured := fixture.ConnectTLS(t, fixture.grant.ProxyCapability.Value(), test.host+":443", test.host)
			defer secured.Close()
			encoder, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
			if err != nil {
				t.Fatal(err)
			}
			defer encoder.Close()
			body := encoder.EncodeAll([]byte(`{"model":"gpt-6-astra","input":"hello"}`), nil)
			response := writeInnerRequest(t, secured, &http.Request{
				Method: http.MethodPost, URL: mustURL(t, test.path), Host: test.host + ":443",
				Header: http.Header{
					"Content-Type": {"application/json"}, "Content-Encoding": {"zstd"},
					"User-Agent": {"codex-tui/0.153.4 (fixture)"},
				},
				Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body)),
			})
			_, err = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if err != nil || response.StatusCode != http.StatusOK {
				t.Fatalf("ingress failed: status=%d, error=%v", response.StatusCode, err)
			}
			requests := fixture.exchanges.Requests()
			if len(requests) != 1 {
				t.Fatalf("semantic dispatch count = %d", len(requests))
			}
			request := requests[0]
			headers, ok := request.OriginalHeaders()
			if !ok || headers.Get("Content-Encoding") != "zstd" || !bytes.Equal(request.Body(), body) ||
				request.RequestPlan().Operation().ID().String() != test.operation {
				t.Fatal("ingress lost the compressed body, encoding, or exact client operation")
			}
			observations := observer.snapshot()
			if len(observations) != 2 {
				t.Fatalf("raw observation count = %d", len(observations))
			}
			ingress := observations[0]
			if ingress.Layer != rawevidence.LayerClientIngress || ingress.ContentEncoding != "zstd" ||
				!ingress.Complete || !bytes.Equal(ingress.Body, body) {
				t.Fatal("Raw HTTP evidence no longer represents the original compressed wire")
			}
		})
	}
}
