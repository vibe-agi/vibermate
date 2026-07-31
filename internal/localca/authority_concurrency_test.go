package localca

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/certidentity"
)

func TestLeafCacheIsBoundedAndSingleflightsOneColdIdentity(t *testing.T) {
	t.Parallel()

	authority := openAuthority(t, filepath.Join(t.TempDir(), "ca"), func(options *Options) {
		options.LeafCacheCapacity = 2
	})
	defer shutdownAuthority(t, authority)
	fixtures := []accessFixture{
		newAccessFixture(t, "one", 1),
		newAccessFixture(t, "two", 1),
		newAccessFixture(t, "three", 1),
	}
	projection := newAccessProjection(t, authority, fixtures...)
	realGenerator := authority.generator
	controlled := newControlledGenerator(realGenerator)
	authority.generator = controlled

	const callers = 32
	release := make(chan struct{})
	controlled.setBarrier(release, false)
	results := make(chan error, callers)
	admissions := make([]access.LeafIssuanceAdmission, callers)
	for index := range callers {
		admissions[index] = leafAdmission(t, projection, authority, fixtures[0])
	}
	for _, admission := range admissions {
		admission := admission
		go func() {
			certificate, err := authority.Issue(
				context.Background(),
				admission,
			)
			if err == nil {
				err = certificate.Leaf.VerifyHostname(
					fixtures[0].origin.TLSServerName(),
				)
			}
			results <- err
		}()
	}
	waitAuthorityCounts(t, authority, callers, 1)
	close(release)
	for range callers {
		if err := <-results; err != nil {
			t.Fatalf("concurrent issuance: %v", err)
		}
	}
	if controlled.callCount() != 1 {
		t.Fatalf("same-key generation count = %d", controlled.callCount())
	}

	controlled.clearBarrier()
	for _, fixture := range fixtures[1:] {
		if _, err := authority.Issue(
			context.Background(),
			leafAdmission(t, projection, authority, fixture),
		); err != nil {
			t.Fatalf("issue %q: %v", fixture.origin.String(), err)
		}
	}
	if authority.cache.Len() != 2 {
		t.Fatalf("bounded cache length = %d", authority.cache.Len())
	}
	if _, err := authority.Issue(
		context.Background(),
		leafAdmission(t, projection, authority, fixtures[0]),
	); err != nil {
		t.Fatalf("reissue evicted leaf: %v", err)
	}
	if controlled.callCount() != 4 {
		t.Fatalf("LRU generation count = %d, want 4", controlled.callCount())
	}
}

func TestDifferentLeafIdentitiesGenerateConcurrently(t *testing.T) {
	t.Parallel()

	authority := openAuthority(t, filepath.Join(t.TempDir(), "ca"), nil)
	defer shutdownAuthority(t, authority)
	first := newAccessFixture(t, "parallel-one", 1)
	second := newAccessFixture(t, "parallel-two", 1)
	projection := newAccessProjection(t, authority, first, second)
	controlled := newControlledGenerator(authority.generator)
	release := make(chan struct{})
	controlled.setBarrier(release, false)
	authority.generator = controlled

	results := make(chan error, 2)
	for _, fixture := range []accessFixture{first, second} {
		fixture := fixture
		admission := leafAdmission(t, projection, authority, fixture)
		go func() {
			_, err := authority.Issue(
				context.Background(),
				admission,
			)
			results <- err
		}()
	}
	waitControlledCalls(t, controlled, 2)
	if controlled.maximumActive() != 2 {
		t.Fatalf("maximum parallel generations = %d", controlled.maximumActive())
	}
	close(release)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("parallel issuance: %v", err)
		}
	}
}

func TestLeafWaiterCancellationDoesNotCancelSharedGeneration(t *testing.T) {
	t.Parallel()

	authority := openAuthority(t, filepath.Join(t.TempDir(), "ca"), nil)
	defer shutdownAuthority(t, authority)
	fixture := newAccessFixture(t, "waiter", 1)
	projection := newAccessProjection(t, authority, fixture)
	controlled := newControlledGenerator(authority.generator)
	release := make(chan struct{})
	controlled.setBarrier(release, false)
	authority.generator = controlled

	firstContext, cancelFirst := context.WithCancel(context.Background())
	firstAdmission := leafAdmission(t, projection, authority, fixture)
	secondAdmission := leafAdmission(t, projection, authority, fixture)
	firstResult := make(chan error, 1)
	go func() {
		_, err := authority.Issue(
			firstContext,
			firstAdmission,
		)
		firstResult <- err
	}()
	waitControlledCalls(t, controlled, 1)
	secondResult := make(chan error, 1)
	go func() {
		_, err := authority.Issue(
			context.Background(),
			secondAdmission,
		)
		secondResult <- err
	}()
	waitAuthorityCounts(t, authority, 2, 1)
	cancelFirst()
	if err := <-firstResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter error = %v", err)
	}
	close(release)
	if err := <-secondResult; err != nil {
		t.Fatalf("surviving waiter error = %v", err)
	}
	if controlled.callCount() != 1 {
		t.Fatalf("shared generation count = %d", controlled.callCount())
	}
}

func TestLeafFailureAndPanicAreNotCached(t *testing.T) {
	t.Parallel()

	for _, mode := range []controlledFailure{failureError, failurePanic} {
		mode := mode
		t.Run(string(mode), func(t *testing.T) {
			authority := openAuthority(t, filepath.Join(t.TempDir(), "ca"), nil)
			defer shutdownAuthority(t, authority)
			fixture := newAccessFixture(t, "retry-"+string(mode), 1)
			projection := newAccessProjection(t, authority, fixture)
			controlled := newControlledGenerator(authority.generator)
			controlled.failNext(mode)
			release := make(chan struct{})
			controlled.setBarrier(release, false)
			authority.generator = controlled
			const callers = 16
			results := make(chan error, callers)
			admissions := make([]access.LeafIssuanceAdmission, callers)
			for index := range callers {
				admissions[index] = leafAdmission(t, projection, authority, fixture)
			}
			for _, admission := range admissions {
				admission := admission
				go func() {
					_, err := authority.Issue(
						context.Background(),
						admission,
					)
					results <- err
				}()
			}
			waitAuthorityCounts(t, authority, callers, 1)
			waitAuthorityFlightWaiters(t, authority, callers)
			close(release)
			for range callers {
				if err := <-results; !errors.Is(err, ErrLeafGenerationFailed) {
					t.Fatalf("shared failed issuance error = %v", err)
				}
			}
			if authority.cache.Len() != 0 {
				t.Fatalf("failed generation cache length = %d", authority.cache.Len())
			}
			controlled.clearBarrier()
			if _, err := authority.Issue(
				context.Background(),
				leafAdmission(t, projection, authority, fixture),
			); err != nil {
				t.Fatalf("retry issuance: %v", err)
			}
			if _, err := authority.Issue(
				context.Background(),
				leafAdmission(t, projection, authority, fixture),
			); err != nil {
				t.Fatalf("cached retry issuance: %v", err)
			}
			if controlled.callCount() != 2 {
				t.Fatalf("generation count = %d, want 2", controlled.callCount())
			}
			waitAuthorityCounts(t, authority, 0, 0)
		})
	}
}

func TestLeafGenerationTimeoutIsTypedAndRetryable(t *testing.T) {
	t.Parallel()

	authority := openAuthority(t, filepath.Join(t.TempDir(), "ca"), func(options *Options) {
		options.GenerationTimeout = 20 * time.Millisecond
	})
	defer shutdownAuthority(t, authority)
	fixture := newAccessFixture(t, "timeout", 1)
	projection := newAccessProjection(t, authority, fixture)
	controlled := newControlledGenerator(authority.generator)
	controlled.setBarrier(make(chan struct{}), false)
	authority.generator = controlled
	if _, err := authority.Issue(
		context.Background(),
		leafAdmission(t, projection, authority, fixture),
	); !errors.Is(err, ErrLeafGenerationTimedOut) ||
		!errors.Is(err, ErrLeafGenerationFailed) {
		t.Fatalf("timed-out issuance error = %v", err)
	}
	if authority.cache.Len() != 0 {
		t.Fatalf("timed-out generation cache length = %d", authority.cache.Len())
	}
	controlled.clearBarrier()
	if _, err := authority.Issue(
		context.Background(),
		leafAdmission(t, projection, authority, fixture),
	); err != nil {
		t.Fatalf("issuance retry after timeout: %v", err)
	}
}

func TestLeafRandomFailureIsNotCachedAndCanRetry(t *testing.T) {
	t.Parallel()

	randomness := &toggleReader{delegate: rand.Reader}
	authority := openAuthority(t, filepath.Join(t.TempDir(), "ca"), func(options *Options) {
		options.Random = randomness
	})
	defer shutdownAuthority(t, authority)
	fixture := newAccessFixture(t, "random-retry", 1)
	projection := newAccessProjection(t, authority, fixture)
	randomness.setFailed(true)
	if _, err := authority.Issue(
		context.Background(),
		leafAdmission(t, projection, authority, fixture),
	); !errors.Is(err, ErrLeafGenerationFailed) ||
		!errors.Is(err, errInjectedRandom) {
		t.Fatalf("random failure error = %v", err)
	}
	if authority.cache.Len() != 0 {
		t.Fatalf("random failure cache length = %d", authority.cache.Len())
	}
	randomness.setFailed(false)
	if _, err := authority.Issue(
		context.Background(),
		leafAdmission(t, projection, authority, fixture),
	); err != nil {
		t.Fatalf("retry after random failure: %v", err)
	}
}

func TestOwnerCancellationClosesCachedAndColdIssuance(t *testing.T) {
	t.Parallel()

	ownerContext, cancelOwner := context.WithCancel(context.Background())
	directory := filepath.Join(t.TempDir(), "ca")
	options := DefaultOptions(directory, ownerContext)
	authority, err := Open(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	fixture := newAccessFixture(t, "owner", 1)
	projection := newAccessProjection(t, authority, fixture)
	if _, err := authority.Issue(
		context.Background(),
		leafAdmission(t, projection, authority, fixture),
	); err != nil {
		t.Fatalf("prime leaf cache: %v", err)
	}
	cancelOwner()
	if _, err := authority.Issue(
		context.Background(),
		leafAdmission(t, projection, authority, fixture),
	); !errors.Is(err, ErrAuthorityClosed) ||
		!errors.Is(err, context.Canceled) {
		t.Fatalf("owner-canceled issuance error = %v", err)
	}
	shutdownAuthority(t, authority)
}

func TestRevocationCutAllowsAdmittedHandshakeWithoutCacheResurrection(
	t *testing.T,
) {
	t.Parallel()

	authority := openAuthority(t, filepath.Join(t.TempDir(), "ca"), nil)
	defer shutdownAuthority(t, authority)
	revisionOne := newAccessFixture(t, "revoked", 1)
	revisionTwo := newAccessFixture(t, "revoked", 2)
	projection := newAccessProjection(t, authority, revisionOne)
	oldBinding, err := projection.ResolveClientOrigin(revisionOne.origin)
	if err != nil {
		t.Fatal(err)
	}
	oldSAN, err := certidentity.NewDNSName(revisionOne.origin.TLSServerName())
	if err != nil {
		t.Fatal(err)
	}
	oldIntent, err := access.NewLeafIssuanceIntent(
		authority.Identity().Revision(),
		oldBinding,
		oldSAN,
		certidentity.LeafKeyAlgorithmECDSAP256,
	)
	if err != nil {
		t.Fatal(err)
	}
	oldAdmission, err := projection.AdmitLeaf(oldIntent)
	if err != nil {
		t.Fatal(err)
	}
	controlled := newControlledGenerator(authority.generator)
	release := make(chan struct{})
	controlled.setBarrier(release, false)
	authority.generator = controlled
	oldResult := make(chan error, 1)
	go func() {
		_, issueErr := authority.Issue(context.Background(), oldAdmission)
		oldResult <- issueErr
	}()
	waitControlledCalls(t, controlled, 1)

	if err := projection.Publish(revisionTwo.plan); err != nil {
		t.Fatalf("publish endpoint replacement: %v", err)
	}
	if _, err := projection.AdmitLeaf(oldIntent); !errors.Is(
		err,
		access.ErrLeafIssuanceUnauthorized,
	) {
		t.Fatalf("post-cut old admission error = %v", err)
	}
	close(release)
	if err := <-oldResult; err != nil {
		t.Fatalf("pre-cut admitted issuance error = %v", err)
	}
	if authority.cache.Len() != 0 {
		t.Fatalf("revoked worker resurrected %d cache entries", authority.cache.Len())
	}

	controlled.clearBarrier()
	if _, err := authority.Issue(
		context.Background(),
		leafAdmission(t, projection, authority, revisionTwo),
	); err != nil {
		t.Fatalf("issue replacement endpoint leaf: %v", err)
	}
	if _, err := authority.Issue(
		context.Background(),
		leafAdmission(t, projection, authority, revisionTwo),
	); err != nil {
		t.Fatalf("reuse replacement endpoint leaf: %v", err)
	}
	if controlled.callCount() != 2 {
		t.Fatalf("post-revocation generation count = %d, want 2", controlled.callCount())
	}
}

func TestDisabledAccessWithdrawalPurgesCacheAndFailsNewAdmission(t *testing.T) {
	t.Parallel()

	authority := openAuthority(t, filepath.Join(t.TempDir(), "ca"), nil)
	defer shutdownAuthority(t, authority)
	fixture := newAccessFixture(t, "disabled", 1)
	projection := newAccessProjection(t, authority, fixture)
	if _, err := authority.Issue(
		context.Background(),
		leafAdmission(t, projection, authority, fixture),
	); err != nil {
		t.Fatalf("prime enabled endpoint leaf: %v", err)
	}
	if authority.cache.Len() != 1 {
		t.Fatalf("primed cache length = %d", authority.cache.Len())
	}
	binding, err := projection.ResolveClientOrigin(fixture.origin)
	if err != nil {
		t.Fatal(err)
	}
	san, err := certidentity.NewDNSName(fixture.origin.TLSServerName())
	if err != nil {
		t.Fatal(err)
	}
	intent, err := access.NewLeafIssuanceIntent(
		authority.Identity().Revision(),
		binding,
		san,
		certidentity.LeafKeyAlgorithmECDSAP256,
	)
	if err != nil {
		t.Fatal(err)
	}
	preCut, err := projection.AdmitLeaf(intent)
	if err != nil {
		t.Fatal(err)
	}
	if err := projection.Withdraw(fixture.accessID, 2); err != nil {
		t.Fatalf("withdraw disabled Access: %v", err)
	}
	if authority.cache.Len() != 0 {
		t.Fatalf("disabled Access cache length = %d", authority.cache.Len())
	}
	if _, err := projection.AdmitLeaf(intent); !errors.Is(
		err,
		access.ErrLeafIssuanceUnauthorized,
	) {
		t.Fatalf("post-disable admission error = %v", err)
	}
	if _, err := authority.Issue(context.Background(), preCut); err != nil {
		t.Fatalf("pre-cut disabled admission could not finish: %v", err)
	}
	if authority.cache.Len() != 0 {
		t.Fatalf("pre-cut disabled worker restored %d cache entries", authority.cache.Len())
	}
}

func TestShutdownDeadlineIsRetryableAndClosesNewAdmission(t *testing.T) {
	t.Parallel()

	authority := openAuthority(t, filepath.Join(t.TempDir(), "ca"), nil)
	fixture := newAccessFixture(t, "shutdown", 1)
	projection := newAccessProjection(t, authority, fixture)
	controlled := newControlledGenerator(authority.generator)
	release := make(chan struct{})
	controlled.setBarrier(release, true)
	authority.generator = controlled
	admission := leafAdmission(t, projection, authority, fixture)
	result := make(chan error, 1)
	go func() {
		_, err := authority.Issue(
			context.Background(),
			admission,
		)
		result <- err
	}()
	waitControlledCalls(t, controlled, 1)

	shutdownContext, cancelShutdown := context.WithTimeout(
		context.Background(),
		20*time.Millisecond,
	)
	defer cancelShutdown()
	if err := authority.Shutdown(shutdownContext); !errors.Is(
		err,
		context.DeadlineExceeded,
	) {
		t.Fatalf("first Shutdown() error = %v", err)
	}
	if _, err := authority.Issue(
		context.Background(),
		leafAdmission(t, projection, authority, fixture),
	); !errors.Is(err, ErrAuthorityClosed) {
		t.Fatalf("post-shutdown admission error = %v", err)
	}
	close(release)
	if err := <-result; !errors.Is(err, ErrLeafGenerationFailed) ||
		!errors.Is(err, ErrAuthorityClosed) {
		t.Fatalf("canceled generation error = %v", err)
	}
	if err := authority.Shutdown(context.Background()); err != nil {
		t.Fatalf("retry Shutdown(): %v", err)
	}
	if err := authority.Shutdown(context.Background()); err != nil {
		t.Fatalf("idempotent Shutdown(): %v", err)
	}
}

type controlledFailure string

const (
	failureNone  controlledFailure = "none"
	failureError controlledFailure = "error"
	failurePanic controlledFailure = "panic"
)

var errInjectedRandom = errors.New("injected random source failure")

type toggleReader struct {
	mu       sync.Mutex
	delegate io.Reader
	failed   bool
}

func (reader *toggleReader) Read(destination []byte) (int, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if reader.failed {
		return 0, errInjectedRandom
	}
	return reader.delegate.Read(destination)
}

func (reader *toggleReader) setFailed(failed bool) {
	reader.mu.Lock()
	reader.failed = failed
	reader.mu.Unlock()
}

type controlledGenerator struct {
	delegate leafGenerator

	mu            sync.Mutex
	calls         int
	active        int
	maxActive     int
	release       <-chan struct{}
	ignoreContext bool
	nextFailure   controlledFailure
	changed       chan struct{}
}

func newControlledGenerator(delegate leafGenerator) *controlledGenerator {
	return &controlledGenerator{
		delegate: delegate,
		changed:  make(chan struct{}),
	}
}

func (generator *controlledGenerator) setBarrier(
	release <-chan struct{},
	ignoreContext bool,
) {
	generator.mu.Lock()
	generator.release = release
	generator.ignoreContext = ignoreContext
	generator.notifyLocked()
	generator.mu.Unlock()
}

func (generator *controlledGenerator) clearBarrier() {
	generator.setBarrier(nil, false)
}

func (generator *controlledGenerator) failNext(mode controlledFailure) {
	generator.mu.Lock()
	generator.nextFailure = mode
	generator.notifyLocked()
	generator.mu.Unlock()
}

func (generator *controlledGenerator) Generate(
	ctx context.Context,
	request access.LeafIssuanceRequest,
) (tlsCertificate, error) {
	generator.mu.Lock()
	generator.calls++
	generator.active++
	if generator.active > generator.maxActive {
		generator.maxActive = generator.active
	}
	release := generator.release
	ignoreContext := generator.ignoreContext
	failure := generator.nextFailure
	generator.nextFailure = failureNone
	generator.notifyLocked()
	generator.mu.Unlock()
	defer func() {
		generator.mu.Lock()
		generator.active--
		generator.notifyLocked()
		generator.mu.Unlock()
	}()

	if release != nil {
		if ignoreContext {
			<-release
		} else {
			select {
			case <-release:
			case <-ctx.Done():
				return tlsCertificate{}, context.Cause(ctx)
			}
		}
	}
	switch failure {
	case failureError:
		return tlsCertificate{}, errors.New("injected signing failure")
	case failurePanic:
		panic("injected signer panic")
	}
	return generator.delegate.Generate(ctx, request)
}

func (generator *controlledGenerator) callCount() int {
	generator.mu.Lock()
	defer generator.mu.Unlock()
	return generator.calls
}

func (generator *controlledGenerator) maximumActive() int {
	generator.mu.Lock()
	defer generator.mu.Unlock()
	return generator.maxActive
}

func (generator *controlledGenerator) notifyLocked() {
	close(generator.changed)
	generator.changed = make(chan struct{})
}

func waitControlledCalls(
	t *testing.T,
	generator *controlledGenerator,
	want int,
) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		generator.mu.Lock()
		if generator.calls >= want {
			generator.mu.Unlock()
			return
		}
		changed := generator.changed
		generator.mu.Unlock()
		select {
		case <-changed:
		case <-deadline.C:
			t.Fatalf("generation calls did not reach %d", want)
		}
	}
}

func waitAuthorityCounts(
	t *testing.T,
	authority *Authority,
	waiters int,
	generations int,
) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		authority.stateMu.Lock()
		if authority.activeWaiters == waiters &&
			authority.activeGenerations == generations {
			authority.stateMu.Unlock()
			return
		}
		changed := authority.changed
		authority.stateMu.Unlock()
		select {
		case <-changed:
		case <-deadline.C:
			authority.stateMu.Lock()
			actualWaiters := authority.activeWaiters
			actualGenerations := authority.activeGenerations
			authority.stateMu.Unlock()
			t.Fatalf(
				"authority counts = waiters:%d generations:%d, want %d/%d",
				actualWaiters,
				actualGenerations,
				waiters,
				generations,
			)
		}
	}
}

func waitAuthorityFlightWaiters(
	t *testing.T,
	authority *Authority,
	want int,
) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		authority.stateMu.Lock()
		if authority.activeFlightWaiters == want {
			authority.stateMu.Unlock()
			return
		}
		changed := authority.changed
		authority.stateMu.Unlock()
		select {
		case <-changed:
		case <-deadline.C:
			authority.stateMu.Lock()
			actual := authority.activeFlightWaiters
			authority.stateMu.Unlock()
			t.Fatalf("active flight waiters = %d, want %d", actual, want)
		}
	}
}
