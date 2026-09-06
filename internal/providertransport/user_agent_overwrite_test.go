package providertransport

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestAccountUserAgentSetAndDeleteReachTheWire(t *testing.T) {
	for _, test := range []struct {
		name   string
		set    map[string]string
		delete []string
		want   string
	}{
		{"set", map[string]string{"User-Agent": "account-agent/7"}, nil, "account-agent/7"},
		{"empty", map[string]string{"User-Agent": ""}, nil, ""},
		{"delete", nil, []string{"User-Agent"}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			gate := newStartedGate(t)
			auth, err := NewStaticBearerAuthenticator(testSecretReaderWithPolicy(t, "secret", test.set, test.delete))
			if err != nil {
				t.Fatal(err)
			}
			transport := &roundTripperStub{response: &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}"))}}
			client, err := NewClient(ClientOptions{Coordinator: gate, Authenticator: auth, Transport: transport})
			if err != nil {
				t.Fatal(err)
			}
			defer shutdownClient(t, client)
			response, _, err := client.Do(context.Background(), newTestRequest(t, gate, "ua-"+test.name, testTarget("provider.example", 443), nil))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.Copy(io.Discard, response.Body); err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			request := transport.lastRequest()
			if got := request.Header.Get("User-Agent"); got != test.want {
				t.Fatalf("wire User-Agent=%q want=%q", got, test.want)
			}
			request.Body = http.NoBody
			request.ContentLength = 0
			var wire bytes.Buffer
			if err := request.Write(&wire); err != nil {
				t.Fatal(err)
			}
			if test.want == "" && strings.Contains(strings.ToLower(wire.String()), "user-agent:") {
				t.Fatal("net/http synthesized User-Agent after explicit omission")
			}
		})
	}
}
