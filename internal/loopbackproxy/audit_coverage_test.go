package loopbackproxy_test

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/connectionevent"
)

// Design 06 section 4.1 requires every attempt to be recorded: allowed,
// denied, timed out, and failed alike, never only the successful ones. A
// rejection that happens before the journal opens is an invisible rejection,
// so an operator investigating "my client cannot reach anything" sees nothing.
func TestNonConnectRequestsAreAuditedBeforeRejection(t *testing.T) {
	t.Parallel()

	fixture := newProxyFixture(t)
	defer fixture.Close(t)

	connection, err := net.DialTimeout(
		"tcp",
		fixture.listener.Addr().String(),
		time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	// An origin-form request is not a proxy request at all, so it is refused
	// as method-not-allowed. An absolute-form request is a cleartext forward
	// and is authenticated first; both must leave evidence of the refusal.
	if _, err := io.WriteString(
		connection,
		"GET / HTTP/1.1\r\nHost: example.com\r\n\r\n",
	); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(
		bufio.NewReader(connection),
		&http.Request{Method: http.MethodGet},
	)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("origin-form status = %d", response.StatusCode)
	}

	page, err := fixture.connections.List(
		context.Background(),
		connectionevent.PageRequest{Limit: 20},
	)
	if err != nil {
		t.Fatal(err)
	}
	attempted := false
	denied := false
	for _, record := range page.Items {
		if record.Phase == connectionevent.PhaseAttempted {
			attempted = true
		}
		if record.Decision == connectionevent.DecisionDeny &&
			strings.Contains(record.ErrorClass, "connect_only") {
			denied = true
		}
	}
	if !attempted || !denied {
		t.Fatalf(
			"cleartext rejection left no audit trail: attempted=%v denied=%v page=%+v",
			attempted,
			denied,
			page,
		)
	}
}

// An unauthenticated cleartext forward is refused, and the refusal is
// recorded rather than dropped.
func TestUnauthenticatedCleartextForwardIsAudited(t *testing.T) {
	t.Parallel()

	fixture := newProxyFixture(t)
	defer fixture.Close(t)

	connection, err := net.DialTimeout(
		"tcp",
		fixture.listener.Addr().String(),
		time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := io.WriteString(
		connection,
		"GET http://example.com/ HTTP/1.1\r\nHost: example.com\r\n\r\n",
	); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(
		bufio.NewReader(connection),
		&http.Request{Method: http.MethodGet},
	)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusProxyAuthRequired {
		t.Fatalf("unauthenticated cleartext status = %d", response.StatusCode)
	}
	page, err := fixture.connections.List(
		context.Background(),
		connectionevent.PageRequest{Limit: 20},
	)
	if err != nil {
		t.Fatal(err)
	}
	denied := false
	for _, record := range page.Items {
		if record.Decision == connectionevent.DecisionDeny &&
			strings.Contains(record.ErrorClass, "proxy_authentication") {
			denied = true
		}
	}
	if !denied {
		t.Fatalf("unauthenticated cleartext left no audit trail: %+v", page)
	}
}
