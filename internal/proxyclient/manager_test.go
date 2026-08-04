package proxyclient

import (
	"context"
	"encoding/base64"
	"errors"
	"sync"
	"testing"
	"time"
)

type blockingRepository struct {
	started chan struct{}
	once    sync.Once
}

func (*blockingRepository) CreateBinding(context.Context, BindingRecord) error {
	return errors.New("unexpected CreateBinding")
}

func (*blockingRepository) CreateEnrollment(context.Context, EnrollmentRecord) error {
	return errors.New("unexpected CreateEnrollment")
}

func (*blockingRepository) CompleteEnrollment(
	context.Context,
	CompletionCandidate,
) (CompletionResult, error) {
	return CompletionResult{}, errors.New("unexpected CompleteEnrollment")
}

func (repository *blockingRepository) Authenticate(
	ctx context.Context,
	_ ControlDigest,
) (AuthenticationRecord, error) {
	repository.once.Do(func() { close(repository.started) })
	<-ctx.Done()
	return AuthenticationRecord{}, context.Cause(ctx)
}

func (*blockingRepository) RevokeBinding(
	context.Context,
	BindingID,
	Revision,
	time.Time,
) (BindingRecord, error) {
	return BindingRecord{}, errors.New("unexpected RevokeBinding")
}

type fixedClock struct{ now time.Time }

func (clock fixedClock) Now() time.Time { return clock.now }

func TestShutdownCancelsAndDrainsActiveAuthentication(t *testing.T) {
	t.Parallel()
	repository := &blockingRepository{started: make(chan struct{})}
	manager, err := NewManager(Options{
		Repository:            repository,
		Clock:                 fixedClock{now: time.Now().UTC()},
		Random:                stringsReader(make([]byte, 512)),
		MaxEnrollmentLifetime: MaximumEnrollmentLifetime,
	})
	if err != nil {
		t.Fatal(err)
	}
	credential, err := ParseControlCredential(
		"control_" + base64.RawURLEncoding.EncodeToString(make([]byte, CredentialBytes)),
	)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, authErr := manager.Authenticate(context.Background(), credential)
		done <- authErr
	}()
	<-repository.started
	shutdownContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Shutdown(shutdownContext); err != nil {
		t.Fatalf("shutdown Manager: %v", err)
	}
	if err := <-done; !errors.Is(err, ErrRuntimeStopping) {
		t.Fatalf("active authentication error = %v", err)
	}
	if _, err := manager.Authenticate(context.Background(), credential); !errors.Is(
		err,
		ErrRuntimeStopping,
	) {
		t.Fatalf("post-shutdown authentication error = %v", err)
	}
}

type stringsReader []byte

func (reader stringsReader) Read(destination []byte) (int, error) {
	for index := range destination {
		destination[index] = reader[index%len(reader)]
	}
	return len(destination), nil
}
