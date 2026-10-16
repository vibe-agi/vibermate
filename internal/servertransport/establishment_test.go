package servertransport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/serverconnection"
)

// Only the external TLS peer is delayed; production target confinement,
// certificate verification and connection establishment remain in use.
func delayedHandshakePeer(t *testing.T) (serverconnection.Target, *x509.CertPool, <-chan struct{}, chan<- struct{}) {
	t.Helper()
	started, release := make(chan struct{}, 1), make(chan struct{})
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("handshake check sent an HTTP request")
	}))
	server.TLS = &tls.Config{GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
		started <- struct{}{}
		<-release
		return nil, nil
	}}
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	target, err := serverconnection.ParseTarget(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return target, roots, started, release
}

func TestRuntimeServerEstablishmentRespectsShortBudgetsAndCancellation(t *testing.T) {
	for _, mode := range []string{"stream", "trust probe"} {
		for _, stop := range []string{"operation timeout", "parent deadline", "caller cancellation"} {
			t.Run(mode+"/"+stop, func(t *testing.T) {
				target, roots, started, release := delayedHandshakePeer(t)
				defer close(release)
				budget, parentBudget := 30*time.Second, 3*time.Second
				if stop == "operation timeout" {
					budget = 100 * time.Millisecond
				}
				if stop == "parent deadline" {
					parentBudget = 100 * time.Millisecond
				}
				ctx, cancel := context.WithTimeout(context.Background(), parentBudget)
				defer cancel()
				result := make(chan error, 1)
				start := time.Now()
				go func() {
					if mode == "trust probe" {
						_, err := ProbeSystemTrust(ctx, SystemTrustProbeOptions{Target: target, Clock: fixedClock{now: time.Now()}, Timeout: budget, RootCAs: roots})
						result <- err
						return
					}
					transport, err := Open(Options{Target: target, TrustDirectory: filepath.Join(t.TempDir(), "trust"), Clock: fixedClock{now: time.Now()}, Timeout: budget, RootCAs: roots})
					if err != nil {
						result <- err
						return
					}
					defer transport.Close()
					connection, err := transport.Dial(ctx)
					if connection != nil {
						_ = connection.Close()
					}
					result <- err
				}()
				select {
				case <-started:
				case <-ctx.Done():
					t.Fatal("TLS handshake did not start")
				}
				if stop == "caller cancellation" {
					cancel()
				}
				err := <-result
				if err == nil || time.Since(start) > time.Second {
					t.Fatalf("short/canceled handshake ignored: elapsed=%v err=%v", time.Since(start), err)
				}
				var timeout net.Error
				if stop == "caller cancellation" {
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("cancellation lost: %v", err)
					}
				} else if !errors.As(err, &timeout) || !timeout.Timeout() {
					t.Fatalf("deadline lost: %v", err)
				}
			})
		}
	}
}

func TestRuntimeServerEstablishmentAllowsSlowTLSHandshake(t *testing.T) {
	for _, mode := range []string{"stream", "trust probe"} {
		t.Run(mode, func(t *testing.T) {
			target, roots, started, release := delayedHandshakePeer(t)
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			options := Options{Target: target, TrustDirectory: filepath.Join(t.TempDir(), "trust"), Clock: fixedClock{now: time.Now()}, Timeout: 30 * time.Second, RootCAs: roots}
			result := make(chan error, 1)
			go func() {
				if mode == "trust probe" {
					_, err := ProbeSystemTrust(ctx, SystemTrustProbeOptions{Target: target, Clock: options.Clock, Timeout: options.Timeout, RootCAs: roots})
					result <- err
					return
				}
				transport, err := Open(options)
				if err != nil {
					result <- err
					return
				}
				defer transport.Close()
				connection, err := transport.Dial(ctx)
				if connection != nil {
					_ = connection.Close()
				}
				result <- err
			}()
			select {
			case <-started:
			case <-ctx.Done():
				close(release)
				t.Fatal("TLS handshake did not start")
			}
			// Intentionally cross the former five-second establishment cap.
			timer := time.NewTimer(5500 * time.Millisecond)
			defer timer.Stop()
			<-timer.C
			close(release)
			if err := <-result; err != nil {
				t.Fatalf("slow verified handshake rejected: %v", err)
			}
		})
	}
}
