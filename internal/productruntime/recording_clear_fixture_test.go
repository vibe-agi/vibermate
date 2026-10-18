package productruntime

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

// The external HTTP test consumes the actual default Runtime and Authority.
// Only the existing optional recovery I/O seam is held until its real return.
func RunRecordingClearAuthorityFixture(t *testing.T, exercise func(*Runtime, *toolapproval.Authority, func() uint64)) {
	t.Helper()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	options := testOptions(t, hostcontract.Desktop(), &coordinatorDouble{})
	r, err := startWithBuilders(t.Context(), options, runtimeBuildOverrides{recoverEgress: func(context.Context, egressaudit.Repository, time.Time) error {
		once.Do(func() { close(entered) })
		<-release
		return nil
	}})
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	defer func() { close(release); shutdownRuntime(t, r) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("actual optional recovery was not entered")
	}
	exercise(r, r.approvals.(*toolapproval.Authority), r.recorder.epoch.Value)
}
