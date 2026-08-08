package desktopdaemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/desktopbootstrap"
	"github.com/vibe-agi/vibermate/internal/desktophost"
	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

func TestStartupFailureClassificationIsClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		err    error
		reason desktopbootstrap.FailureReason
	}{
		{
			name:   "generation already active",
			err:    errors.Join(errors.New("wrapped"), instanceguard.ErrAlreadyOwned),
			reason: desktopbootstrap.FailureRuntimeAlreadyActive,
		},
		{
			name:   "future schema",
			err:    errors.Join(errors.New("wrapped"), runtimepersistence.ErrSchemaNewerThanBinary),
			reason: desktopbootstrap.FailureStorageSchemaNewer,
		},
		{
			name:   "invalid storage",
			err:    runtimepersistence.ErrInvalidDatabasePath,
			reason: desktopbootstrap.FailureStorageUnavailable,
		},
		{
			name:   "unknown runtime",
			err:    errors.New("detail that must not cross the pipe"),
			reason: desktopbootstrap.FailureRuntimeUnavailable,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			failure := classifyStartupFailure(test.err)
			if err := failure.Validate(); err != nil || failure.Reason != test.reason {
				t.Fatalf("failure=%+v validation=%v", failure, err)
			}
		})
	}
}

func TestRunPublishesTypedFailureAfterProgress(t *testing.T) {
	t.Parallel()

	var bootstrap bytes.Buffer
	err := Run(context.Background(), Options{
		Host:            desktophost.Options{},
		BootstrapWriter: &bootstrap,
		ShutdownTimeout: time.Second,
	})
	if err == nil {
		t.Fatal("invalid Host unexpectedly started")
	}
	decoder := json.NewDecoder(&bootstrap)
	var progress desktopbootstrap.Progress
	if err := decoder.Decode(&progress); err != nil || progress.Validate() != nil {
		t.Fatalf("progress=%+v decode=%v", progress, err)
	}
	var failure desktopbootstrap.Failure
	if err := decoder.Decode(&failure); err != nil || failure.Validate() != nil {
		t.Fatalf("failure=%+v decode=%v", failure, err)
	}
	if failure.Reason != desktopbootstrap.FailureRuntimeUnavailable {
		t.Fatalf("failure reason = %q", failure.Reason)
	}
}

func TestParentLifetimeEOFRevokesDaemonOwnership(t *testing.T) {
	t.Parallel()

	reader, writer := io.Pipe()
	ownership, err := NewParentOwnership(context.Background(), reader)
	if err != nil {
		t.Fatal(err)
	}
	defer ownership.Close()

	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ownership.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("parent lifetime EOF did not cancel daemon ownership")
	}
}

func TestParentLifetimeInputFailsClosed(t *testing.T) {
	t.Parallel()

	reader, writer := io.Pipe()
	ownership, err := NewParentOwnership(context.Background(), reader)
	if err != nil {
		t.Fatal(err)
	}
	defer ownership.Close()

	if _, err = writer.Write([]byte{0x01}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ownership.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("parent lifetime input did not fail closed")
	}
}

func TestParentOwnershipCloseDoesNotWaitForInheritedPipeEOF(t *testing.T) {
	t.Parallel()

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	ownership, err := NewParentOwnership(context.Background(), reader)
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() {
		closed <- ownership.Close()
	}()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("parent ownership close waited for external pipe EOF")
	}
	select {
	case <-ownership.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("parent ownership close did not cancel ownership")
	}
}
