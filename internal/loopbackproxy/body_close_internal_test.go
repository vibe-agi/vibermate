package loopbackproxy

import (
	"context"
	"testing"
)

func TestAdmittedBodyCleanupJoinsEnteredClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	body := &heldBodyClose{entered: make(chan struct{}), release: make(chan struct{})}
	join := joinBodyCancellation(ctx, body)
	cancel()
	<-body.entered
	joined := make(chan struct{})
	started := make(chan struct{})
	go func() { close(started); join(); close(joined) }()
	<-started
	select {
	case <-joined:
		t.Fatal("cleanup returned while body Close was held")
	default:
	}
	close(body.release)
	<-joined
}

type heldBodyClose struct{ entered, release chan struct{} }

func (b *heldBodyClose) Close() error { close(b.entered); <-b.release; return nil }
