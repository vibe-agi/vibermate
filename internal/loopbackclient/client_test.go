package loopbackclient_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/loopbackclient"
)

func TestClientPinsLiteralLoopbackAndRejectsRedirects(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.Path == "/redirect" {
			http.Redirect(writer, request, "/target", http.StatusFound)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	server.Listener = listener
	server.Start()
	defer server.Close()

	client, err := loopbackclient.New(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	request, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		server.URL+"/ready",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", response.StatusCode)
	}
	redirect, err := http.NewRequest(
		http.MethodGet,
		server.URL+"/redirect",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do(redirect); err == nil {
		t.Fatal("Client followed a loopback redirect")
	}
}

func TestClientRejectsAliasesAndEscapedRequests(t *testing.T) {
	t.Parallel()

	for _, origin := range []string{
		"",
		"http://localhost:43127",
		"http://127.0.0.1:43127/",
		"http://127.0.0.1:0",
		"http://127.0.0.1:not-a-port",
		"https://127.0.0.1:43127",
		"http://user@127.0.0.1:43127",
	} {
		if _, err := loopbackclient.New(origin, time.Second); err == nil {
			t.Fatalf("New(%q) succeeded", origin)
		}
	}
	client, err := loopbackclient.New(
		"http://127.0.0.1:43127",
		time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	request, err := http.NewRequest(
		http.MethodGet,
		"http://127.0.0.1:43128/status",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do(request); err == nil {
		t.Fatal("Client accepted a request for another loopback origin")
	}
	request.URL.Host = "127.0.0.1:43127"
	request.Host = "127.0.0.1:43128"
	if _, err := client.Do(request); err == nil {
		t.Fatal("Client accepted a confused Host authority")
	}
}
