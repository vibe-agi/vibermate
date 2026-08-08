package loopbackproxy_test

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

// echoTarget stands in for any host an Agent touches that is not a model API:
// a package registry, an update check, an MCP server.
func echoTarget(t *testing.T) (string, func()) {
	t.Helper()

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer connection.Close()
				buffer := make([]byte, 64)
				read, readErr := connection.Read(buffer)
				if readErr != nil {
					return
				}
				_, _ = connection.Write(
					append([]byte("echo:"), buffer[:read]...),
				)
			}()
		}
	}()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return net.JoinHostPort("localhost", port), func() { _ = listener.Close() }
}

// The launcher exports HTTP_PROXY to the whole child process tree, so an Agent
// sends every host through this listener. Refusing the ones that are not model
// APIs makes the product unusable; a blind tunnel forwards them without
// decrypting anything.
func TestUnmatchedAuthorityIsTunnelledWithoutDecryption(t *testing.T) {
	t.Parallel()

	fixture := newProxyFixture(t)
	defer fixture.Close(t)
	authority, stop := echoTarget(t)
	defer stop()

	connection, err := net.DialTimeout(
		"tcp4",
		fixture.listener.Addr().String(),
		2*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	credentials := base64.StdEncoding.EncodeToString(
		[]byte("capture:" + fixture.grant.ProxyCapability.Value()),
	)
	if _, err := io.WriteString(
		connection,
		"CONNECT "+authority+" HTTP/1.1\r\n"+
			"Host: "+authority+"\r\n"+
			"Proxy-Authorization: Basic "+credentials+"\r\n\r\n",
	); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(connection)
	status, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if status != "HTTP/1.1 200 Connection Established\r\n" {
		t.Fatalf("blind CONNECT status = %q", status)
	}
	for {
		line, headerErr := reader.ReadString('\n')
		if headerErr != nil {
			t.Fatal(headerErr)
		}
		if line == "\r\n" {
			break
		}
	}

	if _, err := connection.Write([]byte("opaque-bytes")); err != nil {
		t.Fatal(err)
	}
	echoed := make([]byte, len("echo:opaque-bytes"))
	if _, err := io.ReadFull(reader, echoed); err != nil {
		t.Fatal(err)
	}
	if string(echoed) != "echo:opaque-bytes" {
		t.Fatalf("tunnelled bytes = %q", echoed)
	}
	_ = connection.Close()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		page, listErr := fixture.egress.List(
			context.Background(),
			egressaudit.PageRequest{Limit: 20},
		)
		if listErr != nil {
			t.Fatal(listErr)
		}
		if len(page.Items) == 1 &&
			page.Items[0].Attempt.Terminal() {
			attempt := page.Items[0].Attempt
			if attempt.Purpose() != egressaudit.PurposeBlindTunnel {
				t.Fatalf("blind purpose = %q", attempt.Purpose())
			}
			if attempt.PayloadClass() != egressaudit.PayloadOpaqueTunnel {
				t.Fatalf("blind payload class = %q", attempt.PayloadClass())
			}
			if attempt.Parent().Kind !=
				egressaudit.ParentBlindConnection {
				t.Fatalf("blind parent = %+v", attempt.Parent())
			}
			if attempt.BytesOut() == 0 || attempt.BytesIn() == 0 {
				t.Fatalf("blind byte counts = %+v", attempt)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no blind EgressAttempt reached a terminal")
}

func TestSystemTransparentEnvironmentBypassesConfiguredAskMode(t *testing.T) {
	t.Parallel()

	fixture := newProxyFixtureWithPolicy(t, connectionpolicy.Snapshot{
		Revision: 1,
		Mode:     connectionpolicy.ModeAskUnknown,
	})
	defer fixture.Close(t)
	result := fixture.switchEnvironment(t, environment.SystemTransparentID)
	if result.Boundary != captureassignment.BoundaryHotSwitch {
		t.Fatalf("system transparent switch boundary = %q", result.Boundary)
	}

	authority, stop := echoTarget(t)
	defer stop()
	connection, response := fixture.Connect(
		t,
		fixture.grant.ProxyCapability.Value(),
		authority,
	)
	defer connection.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("transparent CONNECT status = %d", response.StatusCode)
	}
	if _, err := connection.Write([]byte("transparent")); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(connection)
	echoed := make([]byte, len("echo:transparent"))
	if _, err := io.ReadFull(reader, echoed); err != nil {
		t.Fatal(err)
	}
	if string(echoed) != "echo:transparent" {
		t.Fatalf("transparent tunnel payload = %q", echoed)
	}
	page, err := fixture.approvals.ListApprovals(
		context.Background(),
		toolapproval.PageRequest{State: toolapproval.StatePending, Limit: 20},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("system transparent Capture created approvals: %+v", page.Items)
	}
}

// A blind connection still leaves a connection record, and the record still
// contains no path, header, or tunnelled byte.
func TestBlindTunnelRecordsAConnectionWithoutContent(t *testing.T) {
	t.Parallel()

	fixture := newProxyFixture(t)
	defer fixture.Close(t)
	authority, stop := echoTarget(t)
	defer stop()

	connection, _ := fixture.Connect(
		t,
		fixture.grant.ProxyCapability.Value(),
		authority,
	)
	defer connection.Close()
	_, _ = connection.Write([]byte("bytes"))
	_ = connection.Close()

	page, err := fixture.connections.List(
		context.Background(),
		connectionevent.PageRequest{Limit: 20},
	)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, record := range page.Items {
		host, _, splitErr := net.SplitHostPort(authority)
		if splitErr != nil {
			t.Fatal(splitErr)
		}
		if record.RequestedHost != host ||
			record.Phase == connectionevent.PhaseAttempted {
			continue
		}
		found = true
		if record.Decryption != connectionevent.DecryptionBlind {
			t.Fatalf("blind connection decryption = %q", record.Decryption)
		}
		if record.RouteHost == "" {
			t.Fatalf("blind connection has no destination: %+v", record)
		}
		if record.EnvironmentID != fixture.environment.ID ||
			record.EnvironmentName == "" ||
			record.EnvironmentRevision != fixture.environment.Revision ||
			record.ClientEndpointID != "" ||
			record.ClientEndpointRevision != 0 {
			t.Fatalf("blind connection Environment relation = %+v", record)
		}
	}
	if !found {
		t.Fatal("a blind connection left no connection record")
	}
}

// Launching a child proves only that a child was launched. A run reports as
// observed only once authenticated traffic actually arrives through it, so a
// program that ignores proxy variables or dials directly is reported honestly
// rather than as captured.
func TestCaptureRunIsObservedOnlyAfterRealTraffic(t *testing.T) {
	t.Parallel()

	fixture := newProxyFixture(t)
	defer fixture.Close(t)
	authority, stop := echoTarget(t)
	defer stop()

	first, err := fixture.runs.AuthorizeProxy(
		context.Background(),
		mustProxyCapability(t, fixture.grant.ProxyCapability.Value()),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Observed || first.FirstObservedAt.IsZero() {
		t.Fatalf("authenticated traffic did not mark observation: %+v", first)
	}

	// A later connection must not move the first-observed time.
	connection, _ := fixture.Connect(
		t,
		fixture.grant.ProxyCapability.Value(),
		authority,
	)
	_ = connection.Close()

	second, err := fixture.runs.AuthorizeProxy(
		context.Background(),
		mustProxyCapability(t, fixture.grant.ProxyCapability.Value()),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !second.FirstObservedAt.Equal(first.FirstObservedAt) {
		t.Fatalf(
			"a later connection moved the observation from %v to %v",
			first.FirstObservedAt,
			second.FirstObservedAt,
		)
	}
}

func mustProxyCapability(
	t *testing.T,
	value string,
) capturerun.ProxyCapability {
	t.Helper()

	capability, err := capturerun.NewProxyCapability(value)
	if err != nil {
		t.Fatal(err)
	}
	return capability
}
