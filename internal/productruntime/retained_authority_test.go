package productruntime

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/recording"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
)

func TestReleaseAuditPersistentCaptureRevocation(t *testing.T) {
	for _, family := range []string{"semantic", "managed_read"} {
		for _, kind := range []string{"manual", "managed"} {
			t.Run(family+"/"+kind, func(t *testing.T) {
				f := newCapturedOriginalFixture(t, capturedOriginalConfig{family: family, choice: "builtin"})
				f.transport.DisableKeepAlives = false
				stop := func() {
					id, _ := manualcapture.ParseID(f.capture.ID)
					if _, err := f.r.ManualCaptures().Revoke(t.Context(), manualcapture.RevokeCommand{Owner: manualcapture.NewLocalOwnerScope(), ID: id, ExpectedCredentialRevision: 1}); err != nil {
						t.Fatal(err)
					}
				}
				if kind == "managed" {
					dir := t.TempDir()
					grant, err := f.r.captureRuns.Create(t.Context(), capturerun.CreateCommand{CWD: dir, CanonicalExecutablePath: filepath.Join(dir, "agent"), ExecutableLabel: "agent", Lifetime: time.Minute, CatalogRevision: 1})
					if err != nil {
						t.Fatal(err)
					}
					ref, _ := captureidentity.New(captureidentity.KindManagedRun, grant.Run.ID)
					if _, err := f.r.CaptureAssignments().Create(t.Context(), captureassignment.CreateCommand{Capture: ref, EnvironmentID: f.aggregate.ID, Source: captureassignment.SourceLaunch}); err != nil {
						t.Fatal(err)
					}
					proxy, _ := url.Parse(f.listener.URL)
					proxy.User = url.UserPassword("capture", grant.ProxyCapability.Value())
					f.transport.Proxy = http.ProxyURL(proxy)
					stop = func() {
						if err := f.r.captureRuns.Finish(t.Context(), grant.Run.ID, grant.ControlCapability); err != nil {
							t.Fatal(err)
						}
						if _, err := f.r.captureRuns.AuthorizeProxy(t.Context(), grant.ProxyCapability); err == nil {
							t.Fatal("new admission still allowed: test precondition failed")
						}
					}
				}
				method, path, body := "GET", "/backend-api/wham/usage", ""
				if family == "semantic" {
					method, path, body = "POST", semanticOriginalPath, semanticOriginalBody
				}
				first := f.send(t.Context(), method, path, body)
				if first.Err != nil || first.Status != 200 {
					t.Fatalf("baseline failed: %+v", first)
				}
				reads := f.secrets.reads.Load()
				stop()
				reused := false
				ctx := httptrace.WithClientTrace(t.Context(), &httptrace.ClientTrace{GotConn: func(i httptrace.GotConnInfo) { reused = i.Reused }})
				second := f.send(ctx, method, path, body)
				t.Logf("post revocation reused=%v status=%d upstream_calls=%d error=%v", reused, second.Status, f.httpCalls.Load(), second.Err)
				if second.Status == 200 || f.httpCalls.Load() != 1 || f.secrets.reads.Load() != reads {
					t.Fatal("RELEASE BLOCKER: revoked Capture sent a new operation through the previously admitted connection")
				}
			})
		}
	}
}

// An idle tunnel has no next HTTP operation at which to discover withdrawal.
func TestRetainedAuthorityClosesBlindTunnel(t *testing.T) {
	for _, action := range []string{"revoke", "rotate", "finish", "expiry", "shorten"} {
		t.Run(action, func(t *testing.T) {
			f := newB4Fixture(t, 443)
			var stop func()
			var run capturerun.LaunchGrant
			if action == "finish" || action == "expiry" || action == "shorten" {
				dir := t.TempDir()
				lifetime := time.Minute
				if action == "expiry" {
					lifetime = 350 * time.Millisecond
				}
				var err error
				run, err = f.r.captureRuns.Create(t.Context(), capturerun.CreateCommand{CWD: dir, CanonicalExecutablePath: filepath.Join(dir, "agent"), ExecutableLabel: "agent", Lifetime: lifetime, CatalogRevision: 1})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.r.captureRuns.Attach(t.Context(), run.Run.ID, run.ControlCapability, 12345); err != nil {
					t.Fatal(err)
				}
				ref, _ := captureidentity.New(captureidentity.KindManagedRun, run.Run.ID)
				if _, err := f.r.CaptureAssignments().Create(t.Context(), captureassignment.CreateCommand{Capture: ref, EnvironmentID: environment.SystemTransparentID, Source: captureassignment.SourceSystemTransparent}); err != nil {
					t.Fatal(err)
				}
				f.auth = "Basic " + base64.StdEncoding.EncodeToString([]byte("capture:"+run.ProxyCapability.Value()))
				stop = func() {
					if action == "finish" {
						if err := f.r.captureRuns.Finish(t.Context(), run.Run.ID, run.ControlCapability); err != nil {
							t.Fatal(err)
						}
					}
					if action == "shorten" {
						if _, err := f.r.captureRuns.Heartbeat(t.Context(), run.Run.ID, run.ControlCapability, 100*time.Millisecond); err != nil {
							t.Fatal(err)
						}
					}
				}
			} else {
				page, err := f.r.ManualCaptures().List(t.Context(), manualcapture.PageRequest{Owner: manualcapture.NewLocalOwnerScope()})
				if err != nil || len(page.Items) != 1 {
					t.Fatalf("captures=%+v err=%v", page, err)
				}
				id, _ := manualcapture.ParseID(page.Items[0].ID)
				stop = func() {
					if action == "revoke" {
						if _, err := f.r.ManualCaptures().Revoke(t.Context(), manualcapture.RevokeCommand{Owner: manualcapture.NewLocalOwnerScope(), ID: id, ExpectedCredentialRevision: 1}); err != nil {
							t.Fatal(err)
						}
					} else {
						if _, err := f.r.ManualCaptures().Rotate(t.Context(), manualcapture.RotateCommand{Owner: manualcapture.NewLocalOwnerScope(), ID: id, ExpectedCredentialRevision: 1}); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			conn, err := net.DialTimeout("tcp", f.server.Listener.Addr().String(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(2 * time.Second))
			fmt.Fprintf(conn, "CONNECT blind.fixture:443 HTTP/1.1\r\nHost: blind.fixture:443\r\nProxy-Authorization: %s\r\n\r\n", f.auth)
			reader := bufio.NewReader(conn)
			response, err := http.ReadResponse(reader, &http.Request{Method: "CONNECT"})
			if err != nil || response.StatusCode != 200 {
				t.Fatalf("CONNECT=%v err=%v", response, err)
			}
			if _, err = conn.Write([]byte("alive")); err != nil {
				t.Fatal(err)
			}
			reply := make([]byte, 5)
			if _, err = io.ReadFull(reader, reply); err != nil || string(reply) != "alive" {
				t.Fatalf("echo=%q err=%v", reply, err)
			}
			stop()
			conn.SetReadDeadline(time.Now().Add(700 * time.Millisecond))
			_, err = reader.ReadByte()
			if err == nil {
				t.Fatal("withdrawn tunnel returned bytes")
			}
			if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				t.Fatal("withdrawn idle tunnel remained open")
			}
		})
	}
}

func retainedBlindClient(t *testing.T, f *b4Fixture, credential string, socks bool) (net.Conn, *bufio.Reader) {
	t.Helper()
	address := f.server.Listener.Addr().String()
	if socks {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() { defer close(done); _ = f.r.socks.Serve(listener) }()
		t.Cleanup(func() { listener.Close(); b4Join(t, done, "SOCKS listener") })
		address = listener.Addr().String()
	}
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	reader := bufio.NewReader(conn)
	if socks {
		conn.Write([]byte{5, 1, 2})
		var reply [2]byte
		if _, err = io.ReadFull(reader, reply[:]); err != nil || reply != [2]byte{5, 2} {
			t.Fatalf("SOCKS greeting %v %v", reply, err)
		}
		auth := append([]byte{1, 7}, []byte("capture")...)
		auth = append(auth, byte(len(credential)))
		auth = append(auth, []byte(credential)...)
		conn.Write(auth)
		if _, err = io.ReadFull(reader, reply[:]); err != nil || reply != [2]byte{1, 0} {
			t.Fatalf("SOCKS auth %v %v", reply, err)
		}
		target := append([]byte{5, 1, 0, 3, 13}, []byte("blind.fixture")...)
		target = append(target, 1, 187)
		conn.Write(target)
		result := make([]byte, 10)
		if _, err = io.ReadFull(reader, result); err != nil || result[1] != 0 {
			t.Fatalf("SOCKS CONNECT %v %v", result, err)
		}
	} else {
		auth := "Basic " + base64.StdEncoding.EncodeToString([]byte("capture:"+credential))
		fmt.Fprintf(conn, "CONNECT blind.fixture:443 HTTP/1.1\r\nHost: blind.fixture:443\r\nProxy-Authorization: %s\r\n\r\n", auth)
		response, err := http.ReadResponse(reader, &http.Request{Method: "CONNECT"})
		if err != nil || response.StatusCode != 200 {
			t.Fatalf("CONNECT=%v err=%v", response, err)
		}
	}
	retainedEcho(t, conn, reader)
	return conn, reader
}
func retainedEcho(t *testing.T, conn net.Conn, reader *bufio.Reader) {
	t.Helper()
	conn.SetDeadline(time.Now().Add(time.Second))
	if _, err := conn.Write([]byte("alive")); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 5)
	if _, err := io.ReadFull(reader, reply); err != nil || string(reply) != "alive" {
		t.Fatalf("echo=%q err=%v", reply, err)
	}
	conn.SetDeadline(time.Time{})
}
func retainedClosed(t *testing.T, conn net.Conn, reader *bufio.Reader) {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
	_, err := reader.ReadByte()
	if err == nil {
		t.Fatal("withdrawn connection returned bytes")
	}
	if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("withdrawn idle connection remained open")
	}
}
func retainedManagedRun(t *testing.T, f *b4Fixture, lifetime time.Duration, identity runtimeuser.Identity) capturerun.LaunchGrant {
	t.Helper()
	dir := t.TempDir()
	command := capturerun.CreateCommand{CWD: dir, CanonicalExecutablePath: filepath.Join(dir, "agent"), ExecutableLabel: "agent", Lifetime: lifetime, CatalogRevision: 1}
	if identity.User.ID != "" {
		command.RuntimeUserID = identity.User.ID
		command.RuntimeUsername = identity.User.Username
		command.LoginSessionID = identity.SessionID
		command.DeviceName = identity.DeviceName
	}
	grant, err := f.r.captureRuns.Create(t.Context(), command)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.r.captureRuns.Attach(t.Context(), grant.Run.ID, grant.ControlCapability, 12345); err != nil {
		t.Fatal(err)
	}
	ref, _ := captureidentity.New(captureidentity.KindManagedRun, grant.Run.ID)
	if _, err = f.r.CaptureAssignments().Create(t.Context(), captureassignment.CreateCommand{Capture: ref, EnvironmentID: environment.SystemTransparentID, Source: captureassignment.SourceSystemTransparent}); err != nil {
		t.Fatal(err)
	}
	return grant
}
func retainedLogin(t *testing.T, r *Runtime, lifetime time.Duration) runtimeuser.LoginSession {
	t.Helper()
	password := []byte("synthetic retained authority password")
	if _, err := r.RuntimeUsers().Create(t.Context(), runtimeuser.CreateCommand{Username: "retained-user", Password: password}); err != nil {
		t.Fatal(err)
	}
	manager := r.RuntimeUsers()
	if lifetime != 0 {
		var err error
		manager, err = runtimeuser.New(runtimeuser.Options{Repository: r.storage.RuntimeUserRepository(), Clock: r.clock, Random: rand.Reader, SessionLifetime: lifetime})
		if err != nil {
			t.Fatal(err)
		}
	}
	machine, _ := workspaceidentity.ParseMachineID(base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)))
	session, err := manager.Login(t.Context(), runtimeuser.LoginCommand{Username: "retained-user", Password: password, MachineID: machine, DeviceName: "synthetic-device"})
	if err != nil {
		t.Fatal(err)
	}
	return session
}
func TestRetainedAuthorityOriginatingSession(t *testing.T) {
	for _, action := range []string{"logout", "disable", "reset", "session_expiry"} {
		t.Run(action, func(t *testing.T) {
			f := newB4Fixture(t, 443)
			lifetime := time.Duration(0)
			if action == "session_expiry" {
				lifetime = time.Second
			}
			session := retainedLogin(t, f.r, lifetime)
			identity, err := f.r.RuntimeUsers().Authenticate(t.Context(), session.Token.Value())
			if err != nil {
				t.Fatal(err)
			}
			grant := retainedManagedRun(t, f, time.Minute, identity)
			conn, reader := retainedBlindClient(t, f, grant.ProxyCapability.Value(), false)
			if _, err = f.r.captureRuns.Heartbeat(t.Context(), grant.Run.ID, grant.ControlCapability, 2*time.Minute); err != nil {
				t.Fatal(err)
			}
			switch action {
			case "logout":
				err = f.r.RuntimeUsers().Logout(t.Context(), session.Token.Value())
			case "disable":
				_, err = f.r.RuntimeUsers().Disable(t.Context(), session.User.ID)
			case "reset":
				_, err = f.r.RuntimeUsers().ReplacePassword(t.Context(), session.User.ID, []byte("synthetic replacement password"))
			}
			if err != nil {
				t.Fatal(err)
			}
			retainedClosed(t, conn, reader)
		})
	}
}
func TestRetainedAuthorityHeartbeatRenewsAndSOCKSInheritsWithdrawal(t *testing.T) {
	f := newB4Fixture(t, 443)
	grant := retainedManagedRun(t, f, 350*time.Millisecond, runtimeuser.Identity{})
	conn, reader := retainedBlindClient(t, f, grant.ProxyCapability.Value(), true)
	if _, err := f.r.captureRuns.Heartbeat(t.Context(), grant.Run.ID, grant.ControlCapability, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	time.Sleep(450 * time.Millisecond)
	retainedEcho(t, conn, reader)
	if _, err := f.r.captureRuns.Heartbeat(t.Context(), grant.Run.ID, grant.ControlCapability, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	retainedClosed(t, conn, reader)
}

func TestRetainedAuthorityH2RotationPreservesOtherCapturesAndNewGeneration(t *testing.T) {
	f := newCapturedOriginalFixture(t, capturedOriginalConfig{family: "managed_read", choice: "builtin"})
	f.transport.DisableKeepAlives = false
	f.transport.ForceAttemptHTTP2 = true
	var negotiated string
	ctx := httptrace.WithClientTrace(t.Context(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
		if conn, ok := info.Conn.(*tls.Conn); ok {
			negotiated = conn.ConnectionState().NegotiatedProtocol
		}
	}})
	if reply := f.send(ctx, "GET", "/backend-api/wham/usage", ""); reply.Err != nil || reply.Status != 200 {
		t.Fatalf("baseline=%+v", reply)
	}
	if negotiated != "h2" {
		t.Fatalf("negotiated %q", negotiated)
	}
	oldClient := f.client
	other, err := f.r.ManualCaptures().Create(t.Context(), manualcapture.CreateCommand{Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "unaffected capture", ClientClass: manualcapture.ClientCLI, Lifetime: manualcapture.LifetimeUntilRevoked})
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := captureidentity.New(captureidentity.KindManualCapture, other.Capture.ID)
	if _, err = f.r.CaptureAssignments().Create(t.Context(), captureassignment.CreateCommand{Capture: ref, EnvironmentID: f.aggregate.ID, Source: captureassignment.SourceManualCreate}); err != nil {
		t.Fatal(err)
	}
	clientFor := func(credential string) *http.Client {
		transport := f.transport.Clone()
		proxy, _ := url.Parse(f.listener.URL)
		proxy.User = url.UserPassword("capture", credential)
		transport.Proxy = http.ProxyURL(proxy)
		t.Cleanup(transport.CloseIdleConnections)
		return &http.Client{Transport: transport, Timeout: 3 * time.Second}
	}
	otherClient := clientFor(other.Credential.Value())
	f.client = otherClient
	if reply := f.send(t.Context(), "GET", "/backend-api/wham/usage", ""); reply.Err != nil || reply.Status != 200 {
		t.Fatalf("other baseline=%+v", reply)
	}
	id, _ := manualcapture.ParseID(f.capture.ID)
	rotated, err := f.r.ManualCaptures().Rotate(t.Context(), manualcapture.RotateCommand{Owner: manualcapture.NewLocalOwnerScope(), ID: id, ExpectedCredentialRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	before := f.httpCalls.Load()
	reads := f.secrets.reads.Load()
	f.client = oldClient
	if reply := f.send(t.Context(), "GET", "/backend-api/wham/usage", ""); reply.Status == 200 || f.httpCalls.Load() != before || f.secrets.reads.Load() != reads {
		t.Fatalf("old generation retained account authority: %+v", reply)
	}
	f.client = clientFor(rotated.Credential.Value())
	if reply := f.send(t.Context(), "GET", "/backend-api/wham/usage", ""); reply.Err != nil || reply.Status != 200 {
		t.Fatalf("new generation=%+v", reply)
	}
	reused := false
	ctx = httptrace.WithClientTrace(t.Context(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused }})
	f.client = otherClient
	if reply := f.send(ctx, "GET", "/backend-api/wham/usage", ""); reply.Err != nil || reply.Status != 200 || !reused {
		t.Fatalf("unaffected Capture connection lost: %+v reused=%v", reply, reused)
	}
}

func TestRetainedAuthorityRuntimeShutdownClosesAndJoinsBlind(t *testing.T) {
	f := newB4Fixture(t, 443)
	grant := retainedManagedRun(t, f, time.Minute, runtimeuser.Identity{})
	conn, reader := retainedBlindClient(t, f, grant.ProxyCapability.Value(), false)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := f.r.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	retainedClosed(t, conn, reader)
	snapshot := f.gate.Snapshot()
	if snapshot.ActiveActions != 0 || snapshot.ActiveEgress != 0 {
		t.Fatalf("shutdown left tunnel owners: %+v", snapshot)
	}
	// The shared B4 cleanup normally inspects live SQL before shutting down.
	f.r = nil
}

func TestRetainedAuthorityWithdrawalCancelsOpeningDial(t *testing.T) {
	f := newB4Fixture(t, 443)
	grant := retainedManagedRun(t, f, time.Minute, runtimeuser.Identity{})
	barrier := f.n.block(f.target, 1)
	defer barrier.open()
	conn, err := net.DialTimeout("tcp", f.server.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte("capture:"+grant.ProxyCapability.Value()))
	fmt.Fprintf(conn, "CONNECT blind.fixture:443 HTTP/1.1\r\nHost: blind.fixture:443\r\nProxy-Authorization: %s\r\n\r\n", auth)
	select {
	case <-barrier.entered:
	case <-time.After(time.Second):
		t.Fatal("opening dial not reached")
	}
	if err = f.r.captureRuns.Finish(t.Context(), grant.Run.ID, grant.ControlCapability); err != nil {
		t.Fatal(err)
	}
	barrier.open()
	conn.SetReadDeadline(time.Now().Add(time.Second))
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "CONNECT"})
	if err == nil && response.StatusCode == 200 {
		t.Fatal("withdrawn opening dial attached a tunnel")
	}
	entries, physical := f.n.counts(f.target)
	if entries != 1 || physical != 0 {
		t.Fatalf("opening dial dispatched after withdrawal: entries=%d physical=%d", entries, physical)
	}
	snapshot := f.gate.Snapshot()
	if snapshot.ActiveActions != 0 || snapshot.ActiveEgress != 0 {
		t.Fatalf("revocation did not join opening action: %+v", snapshot)
	}
}

func TestRetainedAuthorityLogoutPreservesOtherSessionAndLocalCapture(t *testing.T) {
	f := newB4Fixture(t, 443)
	session := retainedLogin(t, f.r, 0)
	identity, err := f.r.RuntimeUsers().Authenticate(t.Context(), session.Token.Value())
	if err != nil {
		t.Fatal(err)
	}
	first := retainedManagedRun(t, f, time.Minute, identity)
	secondSession, err := f.r.RuntimeUsers().Login(t.Context(), runtimeuser.LoginCommand{Username: session.User.Username, Password: []byte("synthetic retained authority password"), MachineID: session.MachineID, DeviceName: session.DeviceName})
	if err != nil {
		t.Fatal(err)
	}
	secondIdentity, err := f.r.RuntimeUsers().Authenticate(t.Context(), secondSession.Token.Value())
	if err != nil {
		t.Fatal(err)
	}
	second := retainedManagedRun(t, f, time.Minute, secondIdentity)
	local := retainedManagedRun(t, f, time.Minute, runtimeuser.Identity{})
	firstConn, firstReader := retainedBlindClient(t, f, first.ProxyCapability.Value(), false)
	secondConn, secondReader := retainedBlindClient(t, f, second.ProxyCapability.Value(), false)
	localConn, localReader := retainedBlindClient(t, f, local.ProxyCapability.Value(), false)
	if err = f.r.RuntimeUsers().Logout(t.Context(), session.Token.Value()); err != nil {
		t.Fatal(err)
	}
	retainedClosed(t, firstConn, firstReader)
	retainedEcho(t, secondConn, secondReader)
	retainedEcho(t, localConn, localReader)
	// Explicitly finish peers so the shared closed-peer fixture can join cleanly.
	secondConn.Close()
	localConn.Close()
	f.observe("remaining tunnel release", func() bool { return f.gate.Snapshot().ActiveActions == 0 })
}

// The optional sink and real provider Body.Close are held independently.
// Revocation/Finish must join the inner HTTP owner, never optional SQL.
func TestRetainedRevocationJoinsConnectionTerminal(t *testing.T) { runRetainedBodyJoin(t, false) }
func TestRetainedFinishJoinsPreviouslyExpiredConnectionTerminal(t *testing.T) {
	runRetainedBodyJoin(t, true)
}

func runRetainedBodyJoin(t *testing.T, expired bool) {
	t.Helper()
	sink := &blockedObservationStore{entered: make(chan struct{}, 1), release: make(chan struct{})}
	defer close(sink.release)
	body := &cleartextCloseBarrier{entered: make(chan struct{}), release: make(chan struct{})}
	body.block.Store(true)
	var bodyCtx context.Context
	var once sync.Once
	release := func() { once.Do(func() { close(body.release) }) }
	defer release()
	f := newCapturedOriginalFixture(t, capturedOriginalConfig{family: "semantic", choice: "builtin", bodyWrap: func(ctx context.Context, b io.ReadCloser) io.ReadCloser {
		bodyCtx = ctx
		body.ReadCloser = b
		return body
	}, recorder: func(ctx context.Context, b *recording.Budget, _ connectionevent.ScopedStore, _ egressaudit.ScopedAttemptStore) *observationRecorder {
		return newObservationRecorder(ctx, b, sink, sink)
	}})
	f.transport.DisableKeepAlives = false
	waitRecording(t, f.r)
	// Prove the optional worker is blocked before actual authorized Agent traffic.
	event := connectionevent.Event{ConnectionID: "retained-prime", SourceConfidence: connectionevent.SourceConfidenceUnknown, RequestedHost: "example.test", Port: 443, Decryption: connectionevent.DecryptionNone, Phase: connectionevent.PhaseAttempted, StartedAt: time.Now().UTC()}
	if f.r.recorder.ObserveConnection(f.r.recorder.ScopeStamp(captureidentity.Reference{}), event) != recording.Accepted {
		t.Fatal("prime refused")
	}
	<-sink.entered
	var finish func() error
	if expired {
		dir := t.TempDir()
		grant, err := f.r.captureRuns.Create(t.Context(), capturerun.CreateCommand{CWD: dir, CanonicalExecutablePath: filepath.Join(dir, "agent"), ExecutableLabel: "agent", Lifetime: time.Second, CatalogRevision: 1})
		if err != nil {
			t.Fatal(err)
		}
		capture, _ := captureidentity.New(captureidentity.KindManagedRun, grant.Run.ID)
		if _, err = f.r.CaptureAssignments().Create(t.Context(), captureassignment.CreateCommand{Capture: capture, EnvironmentID: f.aggregate.ID, Source: captureassignment.SourceLaunch}); err != nil {
			t.Fatal(err)
		}
		proxy, _ := url.Parse(f.listener.URL)
		proxy.User = url.UserPassword("capture", grant.ProxyCapability.Value())
		f.transport.Proxy = http.ProxyURL(proxy)
		finish = func() error { return f.r.captureRuns.Finish(t.Context(), grant.Run.ID, grant.ControlCapability) }
	} else {
		id, _ := manualcapture.ParseID(f.capture.ID)
		finish = func() error {
			_, err := f.r.ManualCaptures().Revoke(t.Context(), manualcapture.RevokeCommand{Owner: manualcapture.NewLocalOwnerScope(), ID: id, ExpectedCredentialRevision: 1})
			return err
		}
	}
	requestDone := make(chan capturedOriginalReply, 1)
	go func() { requestDone <- f.send(t.Context(), "POST", semanticOriginalPath, semanticOriginalBody) }()
	select {
	case <-body.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("actual provider body Close not entered")
	}
	if expired {
		select {
		case <-bodyCtx.Done():
		case <-time.After(3 * time.Second):
			t.Fatal("expiry did not cancel actual provider owner")
		}
	}
	finished := make(chan error, 1)
	go func() { finished <- finish() }()
	select {
	case <-bodyCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("withdrawal did not cancel real I/O")
	}
	select {
	case err := <-finished:
		t.Fatalf("authority returned before actual Body.Close: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	release()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("authority did not join released body")
	}
	select {
	case <-requestDone:
	case <-time.After(3 * time.Second):
		t.Fatal("actual inner HTTP handler did not return")
	}
	if f.gate.Snapshot().ActiveActions != 0 || f.gate.Snapshot().ActiveEgress != 0 {
		t.Fatal("actual I/O ownership remains", f.gate.Snapshot())
	}
	// Optional sink is still held when execution ownership has joined.
	select {
	case <-sink.release:
		t.Fatal("optional sink released early")
	default:
	}
	if f.httpCalls.Load() != 1 || f.bad.Load() != 0 || f.network.denied.Load() != 0 {
		t.Fatal("withdrawal changed frozen routing/credentials or replayed send")
	}
}
