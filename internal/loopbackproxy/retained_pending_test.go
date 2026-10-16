package loopbackproxy_test

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/captureauthority"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/loopbackproxy"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

type pendingRetainedSource struct {
	captureadmission.CurrentAuthority
	first            atomic.Bool
	entered, release chan struct{}
}

func (s *pendingRetainedSource) Revalidate(ctx context.Context, a captureadmission.Admission) (captureauthority.Validity, error) {
	validity, err := s.CurrentAuthority.Revalidate(ctx, a)
	if s.first.CompareAndSwap(false, true) {
		close(s.entered)
		<-s.release
	}
	return validity, err
}

type pendingRetainedRevoker struct {
	retained *captureadmission.Retained
	changed  chan struct{}
}

func (r *pendingRetainedRevoker) Revoke(ctx context.Context, capture captureidentity.Reference, _ string) error {
	wait := r.retained.BeginChange(captureauthority.Change{Capture: capture})
	close(r.changed)
	return wait(ctx)
}

type pendingRetainedTerminal struct {
	connectionevent.Repository
	entered, release chan struct{}
	once             sync.Once
}

func (r *pendingRetainedTerminal) Append(ctx context.Context, event connectionevent.Event) (connectionevent.Record, error) {
	if event.Phase == connectionevent.PhaseClosed || event.Phase == connectionevent.PhaseFailed {
		r.once.Do(func() { close(r.entered) })
		select {
		case <-r.release:
		case <-ctx.Done():
			return connectionevent.Record{}, ctx.Err()
		}
	}
	return r.Repository.Append(ctx, event)
}
func TestRetainedRejectedRegistrationJoinsTerminal(t *testing.T) {
	source := &pendingRetainedSource{entered: make(chan struct{}), release: make(chan struct{})}
	terminal := &pendingRetainedTerminal{entered: make(chan struct{}), release: make(chan struct{})}
	revoker := &pendingRetainedRevoker{changed: make(chan struct{})}
	var sourceOnce, terminalOnce sync.Once
	releaseSource := func() { sourceOnce.Do(func() { close(source.release) }) }
	releaseTerminal := func() { terminalOnce.Do(func() { close(terminal.release) }) }
	f := newProxyFixtureConfigured(t, protocolspec.DialectAnthropicMessages, nil, allowEverythingTestPolicy(t), nil, "", &proxyFixtureComposition{
		compiler: testEnvironmentCompiler(t, protocolspec.DialectAnthropicMessages), captureRevocation: revoker,
		connectionRepository: func(repository connectionevent.Repository) connectionevent.Repository {
			terminal.Repository = repository
			return terminal
		},
		configureProxy: func(options *loopbackproxy.Options) {
			source.CurrentAuthority = options.Admissions.(captureadmission.CurrentAuthority)
			revoker.retained = captureadmission.NewRetained(options.OwnerContext, source, time.Now)
			options.Admissions = revoker.retained
		},
	})
	defer func() { releaseSource(); releaseTerminal(); f.Close(t); _ = revoker.retained.Shutdown(t.Context()) }()
	conn, err := net.DialTimeout("tcp", f.listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	auth := base64.StdEncoding.EncodeToString([]byte("capture:" + f.grant.ProxyCapability.Value()))
	fmt.Fprintf(conn, "CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: api.anthropic.com:443\r\nProxy-Authorization: Basic %s\r\n\r\n", auth)
	select {
	case <-source.entered:
	case <-time.After(time.Second):
		t.Fatal("final admission read not entered")
	}
	stopped := make(chan error, 1)
	go func() { stopped <- f.runs.Finish(t.Context(), f.grant.Run.ID, f.grant.ControlCapability) }()
	select {
	case <-revoker.changed:
	case <-time.After(time.Second):
		t.Fatal("committed finish did not notify pending admission")
	}
	releaseSource()
	select {
	case <-terminal.entered:
	case <-time.After(time.Second):
		t.Fatal("rejected operation did not enter terminal audit")
	}
	returned := false
	select {
	case err := <-stopped:
		returned = true
		if err != nil {
			t.Error(err)
		}
		t.Error("Finish returned before rejected registered operation's terminal cleanup")
	case <-time.After(30 * time.Millisecond):
	}
	releaseTerminal()
	if !returned {
		select {
		case err := <-stopped:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("Finish did not join released terminal")
		}
	}
	conn.SetReadDeadline(time.Now().Add(time.Second))
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "CONNECT"})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden || len(f.exchanges.Requests()) != 0 || f.original.Count() != 0 {
		t.Fatal("withdrawn pending admission dispatched traffic")
	}
}
