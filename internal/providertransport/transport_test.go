package providertransport

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestIdleReadConnectionBoundsProviderSilence(t *testing.T) {
	t.Parallel()

	clientSide, serverSide := net.Pipe()
	defer serverSide.Close()
	connection := newIdleReadConnection(
		context.Background(),
		clientSide,
		20*time.Millisecond,
	)
	defer connection.Close()
	started := time.Now()
	_, err := connection.Read(make([]byte, 1))
	if !errors.Is(err, ErrProviderResponseIdle) {
		t.Fatalf("Read() error = %v", err)
	}
	var networkError net.Error
	if !errors.As(err, &networkError) || !networkError.Timeout() {
		t.Fatalf("Read() error is not a timeout: %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("idle timeout converged after %s", elapsed)
	}
}

func TestIdleReadConnectionPreservesRequestCancellation(t *testing.T) {
	t.Parallel()

	clientSide, serverSide := net.Pipe()
	defer serverSide.Close()
	ctx, cancel := context.WithCancelCause(context.Background())
	connection := newIdleReadConnection(ctx, clientSide, time.Minute)
	defer connection.Close()
	result := make(chan error, 1)
	go func() {
		_, err := connection.Read(make([]byte, 1))
		result <- err
	}()
	cause := errors.New("provider request canceled")
	cancel(cause)
	select {
	case err := <-result:
		if !errors.Is(err, cause) ||
			errors.Is(err, ErrProviderResponseIdle) {
			t.Fatalf("Read() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("provider read did not converge after cancellation")
	}
}

func TestTransportTimeoutsRejectIncompleteBudgets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*TransportTimeouts)
	}{
		{
			name: "dial",
			mutate: func(timeouts *TransportTimeouts) {
				timeouts.Dial = 0
			},
		},
		{
			name: "TLS handshake",
			mutate: func(timeouts *TransportTimeouts) {
				timeouts.TLSHandshake = 0
			},
		},
		{
			name: "response head",
			mutate: func(timeouts *TransportTimeouts) {
				timeouts.ResponseHead = 0
			},
		},
		{
			name: "response idle",
			mutate: func(timeouts *TransportTimeouts) {
				timeouts.ResponseIdle = 0
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			timeouts := DefaultTransportTimeouts()
			test.mutate(&timeouts)
			if _, err := newProductionStrictTransport(timeouts); err == nil {
				t.Fatal("incomplete provider timeout budgets constructed a transport")
			}
		})
	}
}
