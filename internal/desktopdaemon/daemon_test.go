package desktopdaemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/desktopbootstrap"
	"github.com/vibe-agi/vibermate/internal/desktophost"
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
