package runtimepersistence

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/controlprincipal"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/proxyclient"
)

type proxyClientClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *proxyClientClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *proxyClientClock) Set(value time.Time) {
	clock.mu.Lock()
	clock.now = value
	clock.mu.Unlock()
}

type proxyClientRandom struct {
	mu   sync.Mutex
	next byte
}

func (source *proxyClientRandom) Read(buffer []byte) (int, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	for index := range buffer {
		source.next++
		buffer[index] = source.next
	}
	return len(buffer), nil
}

func testProxyClientPolicy(t *testing.T) proxyclient.BindingPolicy {
	t.Helper()
	policy, err := proxyclient.NewBindingPolicy(
		[]string{"agent-endpoints"},
		[]environment.EnvironmentID{"environment-primary"},
		"quota-default",
		[]controlprincipal.GrantKind{
			controlprincipal.GrantCaptureRun,
			controlprincipal.GrantManualCapture,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func newProxyClientManager(
	t *testing.T,
	repository proxyclient.Repository,
	clock *proxyClientClock,
	random *proxyClientRandom,
) *proxyclient.Manager {
	t.Helper()
	options := proxyclient.DefaultOptions(repository)
	options.Clock = clock
	options.Random = random
	manager, err := proxyclient.NewManager(options)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func createBindingAndEnrollment(
	t *testing.T,
	manager *proxyclient.Manager,
	lifetime time.Duration,
) (proxyclient.BindingView, proxyclient.EnrollmentGrant) {
	t.Helper()
	binding, err := manager.CreateBinding(context.Background(), proxyclient.CreateBindingCommand{
		DisplayName: "Platform team",
		Policy:      testProxyClientPolicy(t),
	})
	if err != nil {
		t.Fatalf("create ProxyClientBinding: %v", err)
	}
	bindingID, err := proxyclient.ParseBindingID(binding.ID)
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := manager.CreateEnrollment(
		context.Background(),
		proxyclient.CreateEnrollmentCommand{
			BindingID:               bindingID,
			ExpectedBindingRevision: 1,
			ExpiresIn:               lifetime,
		},
	)
	if err != nil {
		t.Fatalf("create ClientEnrollmentGrant: %v", err)
	}
	return binding, enrollment
}

func completeEnrollment(
	t *testing.T,
	manager *proxyclient.Manager,
	enrollment proxyclient.EnrollmentGrant,
	machineValue string,
) proxyclient.CompletionGrant {
	t.Helper()
	enrollmentID, err := proxyclient.ParseEnrollmentID(enrollment.Enrollment.ID)
	if err != nil {
		t.Fatal(err)
	}
	machineID, err := proxyclient.ParseMachineID(machineValue)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := manager.CompleteEnrollment(
		context.Background(),
		proxyclient.CompleteEnrollmentCommand{
			EnrollmentID: enrollmentID,
			Credential:   enrollment.Credential,
			MachineID:    machineID,
			DisplayName:  "Alice laptop",
		},
	)
	if err != nil {
		t.Fatalf("complete client enrollment: %v", err)
	}
	return grant
}

func TestProxyClientEnrollmentPersistsDigestsAuthenticatesAndRevokes(t *testing.T) {
	t.Parallel()
	databasePath := filepath.Join(t.TempDir(), "runtime.db")
	clock := &proxyClientClock{now: time.Date(2026, 8, 4, 13, 0, 0, 123456789, time.UTC)}
	random := &proxyClientRandom{}
	store := openTestStore(t, databasePath)
	manager := newProxyClientManager(t, store.ProxyClientRepository(), clock, random)
	binding, enrollment := createBindingAndEnrollment(
		t,
		manager,
		proxyclient.DefaultEnrollmentLifetime,
	)
	if strings.Contains(enrollment.LogValue().String(), enrollment.Credential.Value()) {
		t.Fatal("enrollment structured log exposed its credential")
	}
	var enrollmentDigest []byte
	if err := store.database.QueryRowContext(
		context.Background(),
		`SELECT credential_digest FROM client_enrollments WHERE enrollment_id = ?`,
		enrollment.Enrollment.ID,
	).Scan(&enrollmentDigest); err != nil {
		t.Fatal(err)
	}
	if len(enrollmentDigest) != 32 || string(enrollmentDigest) == enrollment.Credential.Value() {
		t.Fatal("enrollment credential was not stored digest-only")
	}
	if _, err := proxyclient.ParseControlCredential(enrollment.Credential.Value()); err == nil {
		t.Fatal("enrollment credential entered the control namespace")
	}
	completion := completeEnrollment(t, manager, enrollment, "machine-public-1")
	if completion.Principal.Kind() != controlprincipal.KindEnrolledClient ||
		!completion.Principal.Allows(controlprincipal.GrantCaptureRun) ||
		!completion.Principal.Allows(controlprincipal.GrantManualCapture) {
		t.Fatalf("enrolled principal = %+v", completion.Principal)
	}
	if bindingID, ok := completion.Principal.ProxyClientBindingID(); !ok ||
		bindingID != binding.ID {
		t.Fatalf("principal binding = %q, %v", bindingID, ok)
	}
	if strings.Contains(completion.LogValue().String(), completion.Credential.Value()) {
		t.Fatal("completion structured log exposed its credential")
	}
	var controlDigest []byte
	if err := store.database.QueryRowContext(
		context.Background(),
		`SELECT credential_digest FROM enrolled_control_principals WHERE principal_id = ?`,
		completion.Principal.ID(),
	).Scan(&controlDigest); err != nil {
		t.Fatal(err)
	}
	if len(controlDigest) != 32 || string(controlDigest) == completion.Credential.Value() {
		t.Fatal("control credential was not stored digest-only")
	}
	authenticated, err := manager.Authenticate(context.Background(), completion.Credential)
	if err != nil || authenticated.ID() != completion.Principal.ID() {
		t.Fatalf("authenticate principal=%+v err=%v", authenticated, err)
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	shutdownTestStore(t, store)

	reopened := openTestStore(t, databasePath)
	defer shutdownTestStore(t, reopened)
	recovered := newProxyClientManager(t, reopened.ProxyClientRepository(), clock, random)
	defer func() { _ = recovered.Shutdown(context.Background()) }()
	if _, err := recovered.Authenticate(context.Background(), completion.Credential); err != nil {
		t.Fatalf("authenticate after reopen: %v", err)
	}
	bindingID, _ := proxyclient.ParseBindingID(binding.ID)
	revoked, err := recovered.RevokeBinding(
		context.Background(),
		proxyclient.RevokeBindingCommand{BindingID: bindingID, ExpectedRevision: 1},
	)
	if err != nil || revoked.State != proxyclient.BindingRevoked {
		t.Fatalf("revoke binding view=%+v err=%v", revoked, err)
	}
	if _, err := recovered.Authenticate(
		context.Background(),
		completion.Credential,
	); !errors.Is(err, proxyclient.ErrControlRejected) {
		t.Fatalf("authentication after binding revocation = %v", err)
	}
}

func TestProxyClientEnrollmentExpiresAndConcurrentConsumptionHasOneWinner(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	clock := &proxyClientClock{now: time.Date(2026, 8, 4, 14, 0, 0, 0, time.UTC)}
	random := &proxyClientRandom{}
	manager := newProxyClientManager(t, store.ProxyClientRepository(), clock, random)
	defer func() { _ = manager.Shutdown(context.Background()) }()
	_, expired := createBindingAndEnrollment(t, manager, time.Minute)
	clock.Set(clock.Now().Add(time.Minute))
	expiredID, _ := proxyclient.ParseEnrollmentID(expired.Enrollment.ID)
	expiredMachine, _ := proxyclient.ParseMachineID("machine-expired")
	if _, err := manager.CompleteEnrollment(
		context.Background(),
		proxyclient.CompleteEnrollmentCommand{
			EnrollmentID: expiredID,
			Credential:   expired.Credential,
			MachineID:    expiredMachine,
			DisplayName:  "Expired machine",
		},
	); !errors.Is(err, proxyclient.ErrEnrollmentExpired) {
		t.Fatalf("expired enrollment error = %v", err)
	}
	_, enrollment := createBindingAndEnrollment(
		t,
		manager,
		proxyclient.DefaultEnrollmentLifetime,
	)
	enrollmentID, _ := proxyclient.ParseEnrollmentID(enrollment.Enrollment.ID)
	machineID, _ := proxyclient.ParseMachineID("machine-concurrent")
	command := proxyclient.CompleteEnrollmentCommand{
		EnrollmentID: enrollmentID,
		Credential:   enrollment.Credential,
		MachineID:    machineID,
		DisplayName:  "Concurrent machine",
	}
	const callers = 12
	errorsFound := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := manager.CompleteEnrollment(context.Background(), command)
			errorsFound <- err
		}()
	}
	wait.Wait()
	close(errorsFound)
	successes := 0
	for err := range errorsFound {
		if err == nil {
			successes++
			continue
		}
		if !errors.Is(err, proxyclient.ErrEnrollmentConsumed) {
			t.Fatalf("concurrent completion error = %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful concurrent completions = %d", successes)
	}
	var machineCount int
	if err := store.database.QueryRowContext(
		context.Background(),
		`SELECT COUNT(*) FROM machine_registrations WHERE machine_id = ?`,
		machineID.String(),
	).Scan(&machineCount); err != nil {
		t.Fatal(err)
	}
	if machineCount != 1 {
		t.Fatalf("machine registration rows = %d", machineCount)
	}
	var principalCount int
	if err := store.database.QueryRowContext(
		context.Background(),
		`SELECT COUNT(*) FROM enrolled_control_principals AS principal
		 JOIN machine_registrations AS machine
		 ON machine.machine_registration_id = principal.machine_registration_id
		 WHERE machine.machine_id = ?`,
		machineID.String(),
	).Scan(&principalCount); err != nil {
		t.Fatal(err)
	}
	if principalCount != 1 {
		t.Fatalf("enrolled control principal rows = %d", principalCount)
	}
}

func TestProxyClientEnrollmentRejectsWrongCredentialAndBindingRevocation(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	clock := &proxyClientClock{now: time.Date(2026, 8, 4, 14, 30, 0, 0, time.UTC)}
	manager := newProxyClientManager(
		t,
		store.ProxyClientRepository(),
		clock,
		&proxyClientRandom{},
	)
	defer func() { _ = manager.Shutdown(context.Background()) }()
	binding, enrollment := createBindingAndEnrollment(
		t,
		manager,
		proxyclient.DefaultEnrollmentLifetime,
	)
	wrongBytes := make([]byte, proxyclient.CredentialBytes)
	for index := range wrongBytes {
		wrongBytes[index] = 0xa5
	}
	wrongCredential, err := proxyclient.ParseEnrollmentCredential(
		"enroll_" + base64.RawURLEncoding.EncodeToString(wrongBytes),
	)
	if err != nil {
		t.Fatal(err)
	}
	enrollmentID, _ := proxyclient.ParseEnrollmentID(enrollment.Enrollment.ID)
	machineID, _ := proxyclient.ParseMachineID("machine-rejected")
	command := proxyclient.CompleteEnrollmentCommand{
		EnrollmentID: enrollmentID,
		Credential:   wrongCredential,
		MachineID:    machineID,
		DisplayName:  "Rejected machine",
	}
	if _, err := manager.CompleteEnrollment(
		context.Background(),
		command,
	); !errors.Is(err, proxyclient.ErrEnrollmentRejected) {
		t.Fatalf("wrong enrollment credential error = %v", err)
	}
	assertProxyClientEnrollmentStateAndNoIdentity(
		t,
		store.database,
		enrollment.Enrollment.ID,
		proxyclient.EnrollmentActive,
	)
	bindingID, _ := proxyclient.ParseBindingID(binding.ID)
	if _, err := manager.RevokeBinding(
		context.Background(),
		proxyclient.RevokeBindingCommand{BindingID: bindingID, ExpectedRevision: 1},
	); err != nil {
		t.Fatalf("revoke ProxyClientBinding: %v", err)
	}
	command.Credential = enrollment.Credential
	if _, err := manager.CompleteEnrollment(
		context.Background(),
		command,
	); !errors.Is(err, proxyclient.ErrEnrollmentRejected) {
		t.Fatalf("revoked binding enrollment error = %v", err)
	}
	assertProxyClientEnrollmentStateAndNoIdentity(
		t,
		store.database,
		enrollment.Enrollment.ID,
		proxyclient.EnrollmentRevoked,
	)
}

func assertProxyClientEnrollmentStateAndNoIdentity(
	t *testing.T,
	database *sql.DB,
	enrollmentID string,
	wantState proxyclient.EnrollmentState,
) {
	t.Helper()
	var state string
	if err := database.QueryRowContext(
		context.Background(),
		`SELECT state FROM client_enrollments WHERE enrollment_id = ?`,
		enrollmentID,
	).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(wantState) {
		t.Fatalf("client enrollment state = %q, want %q", state, wantState)
	}
	for _, query := range []string{
		`SELECT COUNT(*) FROM machine_registrations`,
		`SELECT COUNT(*) FROM enrolled_control_principals`,
	} {
		var count int
		if err := database.QueryRowContext(context.Background(), query).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("identity rows for %q = %d", query, count)
		}
	}
}

type commitErrorAt struct {
	mu     sync.Mutex
	count  int
	failAt int
}

func (committer *commitErrorAt) Commit(transaction *sql.Tx) error {
	committer.mu.Lock()
	committer.count++
	current := committer.count
	committer.mu.Unlock()
	err := transaction.Commit()
	if err == nil && current == committer.failAt {
		return errors.New("injected post-commit error")
	}
	return err
}

func TestProxyClientCompletionReconcilesCommittedButErroredTransaction(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	committer := &commitErrorAt{failAt: 2}
	repository := newProxyClientRepository(
		store.database,
		store.operations,
		time.Second,
		committer,
	)
	clock := &proxyClientClock{now: time.Date(2026, 8, 4, 15, 0, 0, 0, time.UTC)}
	manager := newProxyClientManager(t, repository, clock, &proxyClientRandom{})
	defer func() { _ = manager.Shutdown(context.Background()) }()
	_, enrollment := createBindingAndEnrollment(
		t,
		manager,
		proxyclient.DefaultEnrollmentLifetime,
	)
	completion := completeEnrollment(t, manager, enrollment, "machine-reconciled")
	if _, err := manager.Authenticate(context.Background(), completion.Credential); err != nil {
		t.Fatalf("authenticate reconciled completion: %v", err)
	}
}

func TestProxyClientEnrollmentCreationReconcilesCommittedButErroredTransaction(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	committer := &commitErrorAt{failAt: 1}
	repository := newProxyClientRepository(
		store.database,
		store.operations,
		time.Second,
		committer,
	)
	clock := &proxyClientClock{now: time.Date(2026, 8, 4, 15, 30, 0, 0, time.UTC)}
	manager := newProxyClientManager(t, repository, clock, &proxyClientRandom{})
	defer func() { _ = manager.Shutdown(context.Background()) }()
	binding, err := manager.CreateBinding(
		context.Background(),
		proxyclient.CreateBindingCommand{
			DisplayName: "Platform team",
			Policy:      testProxyClientPolicy(t),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	bindingID, _ := proxyclient.ParseBindingID(binding.ID)
	enrollment, err := manager.CreateEnrollment(
		context.Background(),
		proxyclient.CreateEnrollmentCommand{
			BindingID:               bindingID,
			ExpectedBindingRevision: 1,
			ExpiresIn:               proxyclient.DefaultEnrollmentLifetime,
		},
	)
	if err != nil {
		t.Fatalf("create reconciled enrollment: %v", err)
	}
	if enrollment.Credential.Value() == "" {
		t.Fatal("reconciled enrollment omitted its one-time credential")
	}
	completeEnrollment(t, manager, enrollment, "machine-after-reconcile")
}
