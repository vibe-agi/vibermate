package runlauncher

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/vibe-agi/vibermate/internal/servercontrol"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/serverconnection"
)

func TestDoctorEntryRejectsUnexpectedAndUnboundedResponses(t *testing.T) {
	for _, test := range []struct{ name, response string }{
		{"unauthenticated-200", "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n"},
		{"CONNECT-405", "HTTP/1.1 405 Method Not Allowed\r\nContent-Length: 0\r\n\r\n"},
		{"unrelated-407", entryResponse(`{"reasonCode":"other"}`)},
		{"HTML-407", entryResponse(`<html>secret-credential</html>`)},
		{"malformed-407", entryResponse(`{"reasonCode":`)},
		{"trailing-407", entryResponse(`{"reasonCode":"proxy_authentication_required"} {}`)},
		{"oversized-body", entryResponse(`{"reasonCode":"proxy_authentication_required","detailCode":"` + strings.Repeat("x", 5000) + `"}`)},
		{"oversized-header", "HTTP/1.1 407 Proxy Authentication Required\r\n" + strings.Repeat("X-Large: "+strings.Repeat("x", 1000)+"\r\n", 20) + "\r\n"},
		{"duplicate-reason", entryResponse(`{"reasonCode":"other","reasonCode":"proxy_authentication_required"}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			target, done := entryPeer(t, func(peer net.Conn) {
				request, err := http.ReadRequest(bufio.NewReader(peer))
				if err != nil {
					t.Error(err)
					return
				}
				if request.Method != "CONNECT" || request.Host != "vibermate-doctor.invalid:443" || request.Header.Get("Authorization") != "" || request.Header.Get("Proxy-Authorization") != "" {
					t.Error("invalid probe request")
				}
				_, _ = io.WriteString(peer, test.response)
			})
			err := ProbeRemoteEntry(context.Background(), target, t.TempDir(), fixedRemoteClock{now: time.Now()}, time.Second)
			if !errors.Is(err, ErrProxyEntry) {
				t.Fatalf("unexpected response accepted: %v", err)
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("peer did not terminate")
			}
		})
	}
}

func entryResponse(body string) string {
	return "HTTP/1.1 407 Proxy Authentication Required\r\nContent-Type: application/json\r\nContent-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body
}

func entryPeer(t *testing.T, serve func(net.Conn)) (serverconnection.Target, <-chan struct{}) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	done := make(chan struct{})
	go func() {
		defer close(done)
		peer, err := listener.Accept()
		if err != nil {
			return
		}
		defer peer.Close()
		_ = peer.SetDeadline(time.Now().Add(3 * time.Second))
		serve(peer)
	}()
	target, err := serverconnection.ParseTarget("http://" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return target, done
}

func TestDoctorEntryBoundsStalledHeaderAndBodyAndTLSAndJoins(t *testing.T) {
	for _, stage := range []string{"header", "body", "tls"} {
		t.Run(stage, func(t *testing.T) {
			target, done := entryPeer(t, func(peer net.Conn) {
				if stage != "tls" {
					if _, err := http.ReadRequest(bufio.NewReader(peer)); err != nil {
						return
					}
					if stage == "body" {
						_, _ = io.WriteString(peer, "HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 100\r\n\r\n{")
					}
				}
				_, _ = io.Copy(io.Discard, peer)
			})
			if stage == "tls" {
				target, _ = serverconnection.ParseTarget(strings.Replace(target.Origin(), "http:", "https:", 1))
			}
			before := time.Now()
			err := ProbeRemoteEntry(context.Background(), target, t.TempDir(), fixedRemoteClock{now: time.Now()}, 50*time.Millisecond)
			if err == nil || time.Since(before) > time.Second {
				t.Fatalf("unbounded stalled probe: %v", err)
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("probe left peer open")
			}
		})
	}
}

func TestDoctorRelayCloseCancelsAndJoinsPendingDial(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pending := &doctorPendingDialer{started: make(chan struct{}), finished: make(chan struct{})}
	relay, err := startLocalServerRelay(&diagnosticDialer{dialer: pending, ctx: ctx})
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	client, err := net.Dial("tcp", strings.TrimPrefix(relay.Origin(), "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	select {
	case <-pending.started:
	case <-time.After(time.Second):
		t.Fatal("dial did not start")
	}
	done := make(chan struct{})
	go func() { relay.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		cancel()
		<-done
		t.Fatal("relay did not cancel diagnostic pending dial")
	}
	select {
	case <-pending.finished:
	default:
		t.Fatal("relay did not join dial")
	}
}

type doctorPendingDialer struct{ started, finished chan struct{} }

func (d *doctorPendingDialer) Dial(ctx context.Context) (net.Conn, error) {
	close(d.started)
	defer close(d.finished)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestDoctorDoesNotEnrollMissingTLSDecisionOrSendBearer(t *testing.T) {
	for _, missing := range []string{"file", "selected-target"} {
		t.Run(missing, func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Millisecond)
			directory := t.TempDir()
			machine, err := remoteLoginMachine(t.Context(), directory, rand.Reader, now)
			if err != nil {
				t.Fatal(err)
			}
			token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x53}, 32))
			var requests atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Path == servercontrol.RuntimeUserCurrentSessionPath {
					_ = json.NewEncoder(w).Encode(servercontrol.RuntimeUserCurrentSession{Schema: servercontrol.RuntimeUserCurrentSessionSchema, InstanceID: "instance.test", APIVersion: "v1", ProductBuild: "test-build", SessionID: "login.test", MachineID: machine, User: servercontrol.RuntimeUserView{ID: "user.test", Username: "alice"}})
					return
				}
				w.WriteHeader(407)
				io.WriteString(w, `{"reasonCode":"proxy_authentication_required"}`)
			}))
			defer server.Close()
			target, err := serverconnection.ParseTarget(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			store, err := serverconnection.OpenLoginStore(filepath.Join(directory, "login"))
			if err != nil {
				t.Fatal(err)
			}
			credential, err := serverconnection.NewLoginCredential(serverconnection.LoginCredentialInput{Target: target, InstanceID: "instance.test", UserID: "user.test", Username: "alice", SessionID: "login.test", SessionToken: token, ExpiresAt: now.Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Save(credential); err != nil {
				t.Fatal(err)
			}
			trustPath := filepath.Join(directory, "trust", "server-pins.json")
			if missing == "selected-target" {
				if err := os.MkdirAll(filepath.Dir(trustPath), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(trustPath, []byte(`{"schema":"vibermate-server-trust-v1","servers":{"localhost:1":{"mode":"system_roots"}}}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(trustPath)
			report, err := DiagnoseRemote(t.Context(), target, directory, fixedRemoteClock{now: now}, "")
			if err != nil || report.Control.Reason != "trust_not_configured" || report.Entry.Checked {
				t.Errorf("diagnosis=%+v err=%v", report, err)
			}
			after, readErr := os.ReadFile(trustPath)
			if !bytes.Equal(before, after) || (missing == "file" && !errors.Is(readErr, os.ErrNotExist)) {
				t.Error("doctor created or changed a trust decision")
			}
			if requests.Load() != 0 {
				t.Errorf("doctor sent %d requests without existing trust", requests.Load())
			}
		})
	}
}

func TestDoctorEntryDoesNotDrainRejectedOrOversizedBodies(t *testing.T) {
	for _, test := range []struct{ name, response string }{
		{"rejected-status", "HTTP/1.1 405 Method Not Allowed\r\nContent-Length: 1000000\r\n\r\nx"},
		{"oversized-body", "HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 1000000\r\n\r\n" + strings.Repeat("x", 4097)},
	} {
		t.Run(test.name, func(t *testing.T) {
			target, done := entryPeer(t, func(peer net.Conn) {
				if _, err := http.ReadRequest(bufio.NewReader(peer)); err != nil {
					return
				}
				_, _ = io.WriteString(peer, test.response)
				_, _ = io.Copy(io.Discard, peer)
			})
			before := time.Now()
			err := ProbeRemoteEntry(t.Context(), target, t.TempDir(), fixedRemoteClock{now: time.Now()}, time.Second)
			if err == nil || time.Since(before) > 500*time.Millisecond {
				t.Fatalf("drained rejected/unbounded body: %v elapsed=%v", err, time.Since(before))
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("peer remained open")
			}
		})
	}
}
