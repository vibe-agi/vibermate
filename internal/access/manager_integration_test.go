package access_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

func TestAccessPlanCASPublishesImmutablePlanAndRestores(t *testing.T) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "data", "runtime.db")
	store := openStore(t, databasePath)
	projection := access.NewSnapshotProjection()
	manager := newManager(t, store, projection)
	accessID := newAccessID(t, "access-primary")

	if _, err := manager.ResolveAccess(accessID); !errors.Is(
		err,
		access.ErrAccessNotConfigured,
	) {
		t.Fatalf("resolve absent Access: %v", err)
	} else {
		assertFailureCode(t, err, access.ReasonAccessNotConfigured)
	}

	cancelledContext, cancelWrite := context.WithCancel(context.Background())
	cancelWrite()
	cancelled, cancelledErr := manager.WriteAccess(
		cancelledContext,
		access.WriteCommand{
			ExpectedRevision: 0,
			Aggregate:        testAggregate(t, accessID, 1, "Cancelled"),
		},
	)
	if cancelled.Outcome != access.WriteOutcomeNotCommitted ||
		!errors.Is(cancelledErr, context.Canceled) {
		t.Fatalf("pre-commit cancellation result=%+v err=%v", cancelled, cancelledErr)
	}
	if _, err := manager.ResolveAccess(accessID); !errors.Is(
		err,
		access.ErrAccessNotConfigured,
	) {
		t.Fatalf("pre-commit cancellation published a plan: %v", err)
	}

	create := access.WriteCommand{
		ExpectedRevision: 0,
		Aggregate:        testAggregate(t, accessID, 1, "Primary"),
	}
	created, err := manager.WriteAccess(context.Background(), create)
	if err != nil {
		t.Fatalf("create Access: %v", err)
	}
	if created.Outcome != access.WriteOutcomeCommitted || created.Revision != 1 {
		t.Fatalf("create result = %+v", created)
	}
	first, err := manager.ResolveAccess(accessID)
	if err != nil {
		t.Fatalf("resolve revision one: %v", err)
	}
	assertCompletePlan(t, first, 1, "Primary")

	// Mutating every mutable input collection after the write cannot alter the
	// compiled active plan.
	create.Aggregate.Binding.ProfileIDs[0] = mustEndpointProfileID(t, "mutated-profile")
	create.Aggregate.Profiles[0].AccountBindingIDs = nil
	create.Aggregate.ProviderTargets[0].Capabilities = nil
	create.Aggregate.RouteSets[0].CandidateProfileIDs = nil
	firstAfterInputMutation, err := manager.ResolveAccess(accessID)
	if err != nil {
		t.Fatalf("resolve after input mutation: %v", err)
	}
	assertCompletePlan(t, firstAfterInputMutation, 1, "Primary")
	assertGetterIsolation(t, firstAfterInputMutation)

	oldHandle := firstAfterInputMutation
	update := access.WriteCommand{
		ExpectedRevision: 1,
		Aggregate:        testAggregate(t, accessID, 2, "Primary updated"),
	}
	updated, err := manager.WriteAccess(context.Background(), update)
	if err != nil {
		t.Fatalf("update Access: %v", err)
	}
	if updated.Outcome != access.WriteOutcomeCommitted || updated.Revision != 2 {
		t.Fatalf("update result = %+v", updated)
	}
	if oldHandle.Revision() != 1 || oldHandle.Binding().Name != "Primary" {
		t.Fatalf(
			"old plan handle changed: revision=%d name=%q",
			oldHandle.Revision(),
			oldHandle.Binding().Name,
		)
	}
	current, err := manager.ResolveAccess(accessID)
	if err != nil {
		t.Fatalf("resolve revision two: %v", err)
	}
	assertCompletePlan(t, current, 2, "Primary updated")
	if current.PlanHash() == oldHandle.PlanHash() {
		t.Fatal("different aggregate revisions produced the same plan hash")
	}

	if err := projection.Publish(oldHandle); !errors.Is(
		err,
		access.ErrPublishedRevisionRegression,
	) {
		t.Fatalf("projection accepted a regressing revision: %v", err)
	}
	afterRegression, err := manager.ResolveAccess(accessID)
	if err != nil {
		t.Fatalf("resolve after regression attempt: %v", err)
	}
	if afterRegression.PlanHash() != current.PlanHash() {
		t.Fatal("regression attempt changed the active plan")
	}

	stale, staleErr := manager.WriteAccess(context.Background(), access.WriteCommand{
		ExpectedRevision: 1,
		Aggregate:        testAggregate(t, accessID, 2, "Stale candidate"),
	})
	if stale.Outcome != access.WriteOutcomeNotCommitted ||
		stale.Revision != 2 ||
		!errors.Is(staleErr, access.ErrRevisionConflict) {
		t.Fatalf("stale CAS result=%+v err=%v", stale, staleErr)
	}
	afterStale, err := manager.ResolveAccess(accessID)
	if err != nil {
		t.Fatalf("resolve after stale CAS: %v", err)
	}
	if afterStale.PlanHash() != current.PlanHash() {
		t.Fatal("stale CAS changed the active plan")
	}

	shutdownManager(t, manager)
	shutdownStore(t, store)

	// This is a normal close/reopen recovery proof, not a process-crash or
	// power-loss durability proof.
	reopened := openStore(t, databasePath)
	defer shutdownStore(t, reopened)
	recoveredManager := newManager(t, reopened, access.NewSnapshotProjection())
	defer shutdownManager(t, recoveredManager)
	recovered, err := recoveredManager.ResolveAccess(accessID)
	if err != nil {
		t.Fatalf("resolve recovered Access: %v", err)
	}
	assertCompletePlan(t, recovered, 2, "Primary updated")
	if recovered.PlanHash() != current.PlanHash() {
		t.Fatalf(
			"recovered plan hash = %s, want %s",
			recovered.PlanHash(),
			current.PlanHash(),
		)
	}
}

func TestConcurrentCASPublishesExactlyOneNewPlan(t *testing.T) {
	t.Parallel()

	store := newTemporaryStore(t)
	defer shutdownStore(t, store)
	manager := newManager(t, store, access.NewSnapshotProjection())
	defer shutdownManager(t, manager)
	accessID := newAccessID(t, "access-concurrent")
	if _, err := manager.WriteAccess(context.Background(), access.WriteCommand{
		ExpectedRevision: 0,
		Aggregate:        testAggregate(t, accessID, 1, "Revision one"),
	}); err != nil {
		t.Fatalf("create initial Access: %v", err)
	}

	type response struct {
		result access.WriteResult
		err    error
	}
	responses := make(chan response, 2)
	start := make(chan struct{})
	commands := []access.WriteCommand{
		{
			ExpectedRevision: 1,
			Aggregate:        testAggregate(t, accessID, 2, "Writer A"),
		},
		{
			ExpectedRevision: 1,
			Aggregate:        testAggregate(t, accessID, 2, "Writer B"),
		},
	}
	for _, command := range commands {
		command := command
		go func() {
			<-start
			result, err := manager.WriteAccess(context.Background(), command)
			responses <- response{result: result, err: err}
		}()
	}
	close(start)

	var committed, conflicts int
	for range 2 {
		response := <-responses
		switch {
		case response.err == nil &&
			response.result.Outcome == access.WriteOutcomeCommitted:
			committed++
		case errors.Is(response.err, access.ErrRevisionConflict) &&
			response.result.Outcome == access.WriteOutcomeNotCommitted:
			conflicts++
		default:
			t.Fatalf(
				"unexpected concurrent CAS result=%+v err=%v",
				response.result,
				response.err,
			)
		}
	}
	if committed != 1 || conflicts != 1 {
		t.Fatalf("committed=%d conflicts=%d, want 1 and 1", committed, conflicts)
	}
	plan, err := manager.ResolveAccess(accessID)
	if err != nil {
		t.Fatalf("resolve concurrent winner: %v", err)
	}
	if plan.Revision() != 2 ||
		(plan.Binding().Name != "Writer A" && plan.Binding().Name != "Writer B") {
		t.Fatalf("active winner revision=%d name=%q", plan.Revision(), plan.Binding().Name)
	}
}

func TestCompileFailureLeavesDurableAndActivePlanUnchanged(t *testing.T) {
	t.Parallel()

	store := newTemporaryStore(t)
	defer shutdownStore(t, store)
	manager := newManager(t, store, access.NewSnapshotProjection())
	defer shutdownManager(t, manager)
	accessID := newAccessID(t, "access-compile-failure")
	if _, err := manager.WriteAccess(context.Background(), access.WriteCommand{
		ExpectedRevision: 0,
		Aggregate:        testAggregate(t, accessID, 1, "Revision one"),
	}); err != nil {
		t.Fatalf("create initial plan: %v", err)
	}
	before, err := manager.ResolveAccess(accessID)
	if err != nil {
		t.Fatalf("resolve initial plan: %v", err)
	}

	invalid := testAggregate(t, accessID, 2, "Invalid revision two")
	invalid.EgressPolicy.Mode = "unknown-egress"
	result, err := manager.WriteAccess(context.Background(), access.WriteCommand{
		ExpectedRevision: 1,
		Aggregate:        invalid,
	})
	if result.Outcome != access.WriteOutcomeNotCommitted ||
		!errors.Is(err, access.ErrInvalidAccessPlan) ||
		!errors.Is(err, access.ErrUnknownEgressMode) {
		t.Fatalf("invalid plan result=%+v err=%v", result, err)
	}
	after, err := manager.ResolveAccess(accessID)
	if err != nil {
		t.Fatalf("resolve after compile failure: %v", err)
	}
	if after.Revision() != 1 || after.PlanHash() != before.PlanHash() {
		t.Fatal("compile failure changed the active plan")
	}
	aggregates, err := store.AccessRepository().LoadAll(context.Background())
	if err != nil {
		t.Fatalf("load after compile failure: %v", err)
	}
	if len(aggregates) != 1 ||
		aggregates[0].Binding.Revision != 1 ||
		aggregates[0].Binding.Name != "Revision one" {
		t.Fatalf("compile failure changed durable state: %+v", aggregates)
	}
}

func TestWriterOwnershipSpansCommitThroughPlanPublication(t *testing.T) {
	t.Parallel()

	store := newTemporaryStore(t)
	defer shutdownStore(t, store)
	projection := newBlockingProjection(1)
	manager := newManager(t, store, projection)
	defer shutdownManager(t, manager)
	accessID := newAccessID(t, "access-serialized-publication")
	firstCommand := access.WriteCommand{
		ExpectedRevision: 0,
		Aggregate:        testAggregate(t, accessID, 1, "Revision one"),
	}
	secondCommand := access.WriteCommand{
		ExpectedRevision: 1,
		Aggregate:        testAggregate(t, accessID, 2, "Revision two"),
	}
	type response struct {
		result access.WriteResult
		err    error
	}
	firstContext, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	firstDone := make(chan response, 1)
	go func() {
		result, err := manager.WriteAccess(firstContext, firstCommand)
		firstDone <- response{result: result, err: err}
	}()
	select {
	case <-projection.entered:
	case <-time.After(time.Second):
		t.Fatal("first writer did not reach publication")
	}
	assertDurableRevision(t, store, 1)
	cancelFirst()

	secondDone := make(chan response, 1)
	go func() {
		result, err := manager.WriteAccess(context.Background(), secondCommand)
		secondDone <- response{result: result, err: err}
	}()
	select {
	case second := <-secondDone:
		t.Fatalf("second writer escaped commit-to-publish ownership: %+v", second)
	case <-time.After(40 * time.Millisecond):
	}
	assertDurableRevision(t, store, 1)

	close(projection.release)
	first := <-firstDone
	second := <-secondDone
	if first.err != nil ||
		first.result.Outcome != access.WriteOutcomeCommitted ||
		first.result.Revision != 1 {
		t.Fatalf("first write result=%+v err=%v", first.result, first.err)
	}
	if second.err != nil ||
		second.result.Outcome != access.WriteOutcomeCommitted ||
		second.result.Revision != 2 {
		t.Fatalf("second write result=%+v err=%v", second.result, second.err)
	}
	active, err := manager.ResolveAccess(accessID)
	if err != nil ||
		active.Revision() != 2 ||
		active.Binding().Name != "Revision two" {
		t.Fatalf(
			"active plan revision=%d name=%q err=%v",
			active.Revision(),
			active.Binding().Name,
			err,
		)
	}
}

func TestPublicationFailurePoisonsOnlyAffectedAccess(t *testing.T) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "data", "runtime.db")
	store := openStore(t, databasePath)
	defer shutdownStore(t, store)
	firstID := newAccessID(t, "access-poisoned")
	secondID := newAccessID(t, "access-independent")
	projection := newFailingProjection(firstID, 2)
	manager := newManager(t, store, projection)
	defer shutdownManager(t, manager)

	for _, accessID := range []access.AccessID{firstID, secondID} {
		result, err := manager.WriteAccess(context.Background(), access.WriteCommand{
			ExpectedRevision: 0,
			Aggregate:        testAggregate(t, accessID, 1, "Revision one"),
		})
		if err != nil || result.Revision != 1 {
			t.Fatalf("create accessId=%q result=%+v err=%v", accessID, result, err)
		}
	}
	oldHandle, err := manager.ResolveAccess(firstID)
	if err != nil {
		t.Fatalf("resolve old handle: %v", err)
	}

	result, err := manager.WriteAccess(context.Background(), access.WriteCommand{
		ExpectedRevision: 1,
		Aggregate:        testAggregate(t, firstID, 2, "Durable revision two"),
	})
	if result.Outcome != access.WriteOutcomeCommitted ||
		result.Revision != 2 ||
		!errors.Is(err, access.ErrProjectionUnavailable) {
		t.Fatalf("publication failure result=%+v err=%v", result, err)
	}
	if _, err := manager.ResolveAccess(firstID); !errors.Is(
		err,
		access.ErrProjectionUnavailable,
	) {
		t.Fatalf("poisoned Access remained resolvable: %v", err)
	}
	if oldHandle.Revision() != 1 || oldHandle.Binding().Name != "Revision one" {
		t.Fatal("previously acquired handle changed after publication failure")
	}
	if _, err := manager.WriteAccess(context.Background(), access.WriteCommand{
		ExpectedRevision: 2,
		Aggregate:        testAggregate(t, firstID, 3, "Rejected revision three"),
	}); !errors.Is(err, access.ErrProjectionUnavailable) {
		t.Fatalf("poisoned Access accepted a new write: %v", err)
	}

	secondResult, err := manager.WriteAccess(context.Background(), access.WriteCommand{
		ExpectedRevision: 1,
		Aggregate:        testAggregate(t, secondID, 2, "Independent revision two"),
	})
	if err != nil || secondResult.Revision != 2 {
		t.Fatalf("independent update result=%+v err=%v", secondResult, err)
	}
	secondPlan, err := manager.ResolveAccess(secondID)
	if err != nil || secondPlan.Revision() != 2 {
		t.Fatalf("resolve independent plan revision=%d err=%v", secondPlan.Revision(), err)
	}

	health := manager.ProjectionHealth()
	if health.State != access.ProjectionStateUnavailable ||
		health.UnavailableAccessCount != 1 {
		t.Fatalf("projection health = %+v", health)
	}

	shutdownManager(t, manager)
	shutdownStore(t, store)
	reopened := openStore(t, databasePath)
	defer shutdownStore(t, reopened)
	recoveredManager := newManager(t, reopened, access.NewSnapshotProjection())
	defer shutdownManager(t, recoveredManager)
	firstRecovered, err := recoveredManager.ResolveAccess(firstID)
	if err != nil ||
		firstRecovered.Revision() != 2 ||
		firstRecovered.Binding().Name != "Durable revision two" {
		t.Fatalf(
			"recovered poisoned Access revision=%d name=%q err=%v",
			firstRecovered.Revision(),
			firstRecovered.Binding().Name,
			err,
		)
	}
	secondRecovered, err := recoveredManager.ResolveAccess(secondID)
	if err != nil ||
		secondRecovered.Revision() != 2 ||
		secondRecovered.Binding().Name != "Independent revision two" {
		t.Fatalf(
			"recovered independent Access revision=%d name=%q err=%v",
			secondRecovered.Revision(),
			secondRecovered.Binding().Name,
			err,
		)
	}
	if health := recoveredManager.ProjectionHealth(); health.State != access.ProjectionStateHealthy {
		t.Fatalf("recovered projection health = %+v", health)
	}
}

func TestConcurrentResolversObserveCompletePlansAcrossAccesses(t *testing.T) {
	store := newTemporaryStore(t)
	defer shutdownStore(t, store)
	manager := newManager(t, store, access.NewSnapshotProjection())
	defer shutdownManager(t, manager)
	accessIDs := []access.AccessID{
		newAccessID(t, "access-race-a"),
		newAccessID(t, "access-race-b"),
	}
	for _, accessID := range accessIDs {
		if _, err := manager.WriteAccess(context.Background(), access.WriteCommand{
			ExpectedRevision: 0,
			Aggregate:        testAggregate(t, accessID, 1, "Revision 1"),
		}); err != nil {
			t.Fatalf("create accessId=%q: %v", accessID, err)
		}
	}

	done := make(chan struct{})
	errs := make(chan error, 16)
	var readers sync.WaitGroup
	for range 16 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				for _, accessID := range accessIDs {
					plan, err := manager.ResolveAccess(accessID)
					if err != nil {
						errs <- err
						return
					}
					if err := validateObservedPlan(plan); err != nil {
						errs <- err
						return
					}
				}
			}
		}()
	}

	for revision := access.Revision(2); revision <= 30; revision++ {
		for _, accessID := range accessIDs {
			if _, err := manager.WriteAccess(context.Background(), access.WriteCommand{
				ExpectedRevision: revision - 1,
				Aggregate: testAggregate(
					t,
					accessID,
					revision,
					fmt.Sprintf("Revision %d", revision),
				),
			}); err != nil {
				close(done)
				readers.Wait()
				t.Fatalf("write accessId=%q revision=%d: %v", accessID, revision, err)
			}
		}
	}
	close(done)
	readers.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent resolver observed invalid plan: %v", err)
	}
	for _, accessID := range accessIDs {
		plan, err := manager.ResolveAccess(accessID)
		if err != nil {
			t.Fatalf("resolve final accessId=%q: %v", accessID, err)
		}
		if plan.Revision() != 30 || plan.Binding().Name != "Revision 30" {
			t.Fatalf(
				"final accessId=%q revision=%d name=%q",
				accessID,
				plan.Revision(),
				plan.Binding().Name,
			)
		}
	}
}

func TestShutdownDeadlineDrainsCommitToPublicationBoundary(t *testing.T) {
	t.Parallel()

	store := newTemporaryStore(t)
	defer shutdownStore(t, store)
	projection := newBlockingProjection(1)
	manager := newManager(t, store, projection)
	accessID := newAccessID(t, "access-blocked-publication")
	command := access.WriteCommand{
		ExpectedRevision: 0,
		Aggregate:        testAggregate(t, accessID, 1, "Committed"),
	}

	writeDone := make(chan error, 1)
	go func() {
		_, err := manager.WriteAccess(context.Background(), command)
		writeDone <- err
	}()
	select {
	case <-projection.entered:
	case <-time.After(time.Second):
		t.Fatal("writer did not reach blocked publication")
	}

	shutdownContext, cancelShutdown := context.WithTimeout(
		context.Background(),
		40*time.Millisecond,
	)
	shutdownErr := manager.Shutdown(shutdownContext)
	cancelShutdown()
	if !errors.Is(shutdownErr, context.DeadlineExceeded) {
		t.Fatalf("blocked Access shutdown error = %v", shutdownErr)
	}
	if _, err := manager.WriteAccess(context.Background(), access.WriteCommand{
		ExpectedRevision: 0,
		Aggregate: testAggregate(
			t,
			newAccessID(t, "access-rejected"),
			1,
			"Rejected",
		),
	}); !errors.Is(err, access.ErrAccessRuntimeStopping) {
		t.Fatalf("Access shutdown accepted a new write: %v", err)
	}

	close(projection.release)
	if err := <-writeDone; err != nil {
		t.Fatalf("committed write failed after publication release: %v", err)
	}
	retryContext, cancelRetry := context.WithTimeout(context.Background(), time.Second)
	defer cancelRetry()
	if err := manager.Shutdown(retryContext); err != nil {
		t.Fatalf("retry Access shutdown: %v", err)
	}
}

func assertCompletePlan(
	t *testing.T,
	plan access.AccessPlanSnapshot,
	revision access.Revision,
	name string,
) {
	t.Helper()
	if plan.Revision() != revision || plan.Binding().Name != name {
		t.Fatalf(
			"plan revision=%d name=%q, want revision=%d name=%q",
			plan.Revision(),
			plan.Binding().Name,
			revision,
			name,
		)
	}
	if plan.PlanHash().IsZero() || len(plan.PlanHash().String()) != 64 {
		t.Fatalf("plan hash is invalid: %q", plan.PlanHash())
	}
	endpoint := plan.AgentEndpoint()
	if endpoint.ClientOrigin.String() !=
		"https://"+plan.AccessID().String()+".example.test:443" ||
		endpoint.ClientDialect != access.DialectAnthropicMessages {
		t.Fatalf("AgentEndpoint = %+v", endpoint)
	}
	profiles := plan.EndpointProfiles()
	if len(profiles) != 1 ||
		profiles[0].BackendDialect != access.DialectOpenAIChat ||
		profiles[0].DefaultModelPolicy.Mode != access.ModelPolicyModeFixed ||
		profiles[0].DefaultModelPolicy.FixedModel.String() != "gpt-4.1-mini" {
		t.Fatalf("profiles = %+v", profiles)
	}
	targets := plan.ProviderTargets()
	if len(targets) != 1 ||
		targets[0].Target().Origin.String() != "https://api.openai.com:443/v1" ||
		targets[0].BasePath() != "/v1" ||
		targets[0].HTTPAuthority() != "api.openai.com:443" ||
		targets[0].TLSServerName() != "api.openai.com" {
		t.Fatalf("compiled targets = %+v", targets)
	}
	accounts := plan.AccountBindings()
	if len(accounts) != 1 ||
		accounts[0].SecretRef.String() != "secret://provider/"+plan.AccessID().String() ||
		accounts[0].AuthDriverRef != access.StaticHeaderAuthDriverRef() {
		t.Fatalf("account bindings = %+v", accounts)
	}
	codec := plan.CodecPlan()
	if codec.ClientDialect() != access.DialectAnthropicMessages ||
		codec.ProviderDialect() != access.DialectOpenAIChat ||
		codec.ID().String() != "anthropic-messages-to-openai-chat" {
		t.Fatalf("codec plan = %+v", codec)
	}
	if plan.EgressPolicy().Mode != access.EgressModeDirect {
		t.Fatalf("egress policy = %+v", plan.EgressPolicy())
	}
	if plugin := plan.PluginPlan(); plugin.Mode() != access.PluginPlanModePassThrough ||
		len(plugin.BindingIDs()) != 0 {
		t.Fatalf("plugin plan mode=%q bindings=%v", plugin.Mode(), plugin.BindingIDs())
	}
	transport := plan.TransportFingerprintPlan()
	requestedTransport := transport.Requested()
	transportFallbacks := transport.Fallbacks()
	if requestedTransport.Ref() != access.ObservedClientH1TransportProfileRef() ||
		requestedTransport.Source() != access.TransportFingerprintObservedClient ||
		requestedTransport.HTTPTransport() != access.HTTPTransportHTTP1 ||
		len(requestedTransport.ALPN()) != 1 ||
		requestedTransport.ALPN()[0] != access.ApplicationProtocolHTTP1 ||
		len(transportFallbacks) != 1 ||
		transportFallbacks[0].Ref() != access.StandardH1TransportProfileRef() ||
		transportFallbacks[0].Source() != access.TransportFingerprintStandard {
		t.Fatalf(
			"transport fingerprint requested=%+v fallbacks=%+v",
			requestedTransport,
			transportFallbacks,
		)
	}
	routeSets := plan.RouteSets()
	if len(routeSets) != 1 ||
		routeSets[0].ID != plan.Binding().DefaultRouteSetID ||
		len(routeSets[0].CandidateProfileIDs) != 1 ||
		routeSets[0].CandidateProfileIDs[0] != profiles[0].ID {
		t.Fatalf("route sets = %+v", routeSets)
	}
	dependencies := plan.DependencyRevisions()
	if len(dependencies) != 16 {
		t.Fatalf(
			"routeSets=%d dependencyRevisions=%d",
			len(routeSets),
			len(dependencies),
		)
	}
	aggregateKinds := map[access.DependencyKind]bool{
		access.DependencyAccessBinding:      true,
		access.DependencyAgentEndpoint:      true,
		access.DependencyEndpointProfile:    true,
		access.DependencyProviderTarget:     true,
		access.DependencyAccountBinding:     true,
		access.DependencyModelPolicy:        true,
		access.DependencyRouteSet:           true,
		access.DependencyAccessEgressPolicy: true,
		access.DependencyPluginPlan:         true,
	}
	seenKinds := make(map[access.DependencyKind]int, len(dependencies))
	for _, dependency := range dependencies {
		seenKinds[dependency.Kind]++
		if aggregateKinds[dependency.Kind] && dependency.Revision != revision {
			t.Fatalf("aggregate dependency = %+v, want revision %d", dependency, revision)
		}
		if !aggregateKinds[dependency.Kind] && dependency.Revision != 1 {
			t.Fatalf("catalog dependency = %+v, want revision 1", dependency)
		}
	}
	for _, kind := range []access.DependencyKind{
		access.DependencyAccessBinding,
		access.DependencyAgentEndpoint,
		access.DependencyEndpointProfile,
		access.DependencyProviderTarget,
		access.DependencyAccountBinding,
		access.DependencyModelPolicy,
		access.DependencyRouteSet,
		access.DependencyAccessEgressPolicy,
		access.DependencyPluginPlan,
		access.DependencyCodecPair,
		access.DependencyAuthDriver,
		access.DependencyEgressCapability,
		access.DependencyPluginPlanCapability,
		access.DependencyModelPolicyCapability,
	} {
		if seenKinds[kind] != 1 {
			t.Fatalf("dependency kind %q count=%d, want 1", kind, seenKinds[kind])
		}
	}
	if seenKinds[access.DependencyTransportFingerprint] != 2 {
		t.Fatalf(
			"transport fingerprint dependency count=%d, want 2",
			seenKinds[access.DependencyTransportFingerprint],
		)
	}
}

func assertGetterIsolation(t *testing.T, plan access.AccessPlanSnapshot) {
	t.Helper()
	binding := plan.Binding()
	binding.ProfileIDs = nil
	profiles := plan.EndpointProfiles()
	profiles[0].AccountBindingIDs = nil
	targets := plan.ProviderTargets()
	target := targets[0].Target()
	target.Capabilities = nil
	routes := plan.RouteSets()
	routes[0].CandidateProfileIDs = nil
	codec := plan.CodecPlan()
	required := codec.RequiredCapabilities()
	required[0] = "mutated"
	transport := plan.TransportFingerprintPlan()
	requestedALPN := transport.Requested().ALPN()
	requestedALPN[0] = access.ApplicationProtocolHTTP2
	fallbacks := transport.Fallbacks()
	fallbackALPN := fallbacks[0].ALPN()
	fallbackALPN[0] = access.ApplicationProtocolHTTP2
	dependencies := plan.DependencyRevisions()
	dependencies[0].ID = "mutated"

	if len(plan.Binding().ProfileIDs) != 1 ||
		len(plan.EndpointProfiles()[0].AccountBindingIDs) != 1 ||
		len(plan.ProviderTargets()[0].Target().Capabilities) != 3 ||
		len(plan.RouteSets()[0].CandidateProfileIDs) != 1 ||
		plan.CodecPlan().RequiredCapabilities()[0] == "mutated" ||
		plan.TransportFingerprintPlan().Requested().ALPN()[0] !=
			access.ApplicationProtocolHTTP1 ||
		plan.TransportFingerprintPlan().Fallbacks()[0].ALPN()[0] !=
			access.ApplicationProtocolHTTP1 ||
		plan.DependencyRevisions()[0].ID == "mutated" {
		t.Fatal("a getter exposed mutable active-plan state")
	}
}

func validateObservedPlan(plan access.AccessPlanSnapshot) error {
	if plan.PlanHash().IsZero() {
		return errors.New("zero PlanHash")
	}
	if plan.Binding().Revision != plan.Revision() {
		return errors.New("binding and plan revisions differ")
	}
	if plan.Binding().Name != fmt.Sprintf("Revision %d", plan.Revision()) {
		return fmt.Errorf(
			"name=%q revision=%d",
			plan.Binding().Name,
			plan.Revision(),
		)
	}
	targets := plan.ProviderTargets()
	if len(targets) != 1 ||
		targets[0].BasePath() != "/v1" ||
		targets[0].Target().Revision != plan.Revision() {
		return errors.New("target is not from the same complete aggregate revision")
	}
	foundRoot := false
	for _, dependency := range plan.DependencyRevisions() {
		if dependency.Kind == access.DependencyAccessBinding {
			foundRoot = true
			if dependency.Revision != plan.Revision() {
				return errors.New("root dependency revision differs")
			}
		}
	}
	if !foundRoot {
		return errors.New("root dependency is missing")
	}
	return nil
}

func assertDurableRevision(
	t *testing.T,
	store *runtimepersistence.Store,
	revision access.Revision,
) {
	t.Helper()
	aggregates, err := store.AccessRepository().LoadAll(context.Background())
	if err != nil {
		t.Fatalf("load durable Access: %v", err)
	}
	if len(aggregates) != 1 || aggregates[0].Binding.Revision != revision {
		t.Fatalf("durable aggregates=%+v, want revision=%d", aggregates, revision)
	}
}

type blockingProjection struct {
	delegate      access.SnapshotProjection
	blockRevision access.Revision
	entered       chan struct{}
	release       chan struct{}
	enterOnce     sync.Once
}

func newBlockingProjection(blockRevision access.Revision) *blockingProjection {
	return &blockingProjection{
		delegate:      access.NewSnapshotProjection(),
		blockRevision: blockRevision,
		entered:       make(chan struct{}),
		release:       make(chan struct{}),
	}
}

func (p *blockingProjection) Restore(plans []access.AccessPlanSnapshot) error {
	return p.delegate.Restore(plans)
}

func (p *blockingProjection) Publish(plan access.AccessPlanSnapshot) error {
	if plan.Revision() == p.blockRevision {
		p.enterOnce.Do(func() {
			close(p.entered)
		})
		<-p.release
	}
	return p.delegate.Publish(plan)
}

func (p *blockingProjection) ResolveAccess(
	accessID access.AccessID,
) (access.AccessPlanSnapshot, error) {
	return p.delegate.ResolveAccess(accessID)
}

func (p *blockingProjection) ResolveClientOrigin(
	origin access.ClientOrigin,
) (access.IngressBinding, error) {
	return p.delegate.ResolveClientOrigin(origin)
}

func (p *blockingProjection) ActiveClientAuthorities() ([]string, error) {
	return p.delegate.ActiveClientAuthorities()
}

func (p *blockingProjection) ActiveProviderProbeTargets() (
	[]access.ProviderProbeTarget,
	error,
) {
	return p.delegate.ActiveProviderProbeTargets()
}

func (p *blockingProjection) MarkUnavailable(accessID access.AccessID) {
	p.delegate.MarkUnavailable(accessID)
}

func (p *blockingProjection) ProjectionHealth() access.ProjectionHealth {
	return p.delegate.ProjectionHealth()
}

var errInjectedPublication = errors.New("injected plan publication error")

type failingProjection struct {
	delegate     access.SnapshotProjection
	failAccessID access.AccessID
	failRevision access.Revision
}

func newFailingProjection(
	accessID access.AccessID,
	revision access.Revision,
) *failingProjection {
	return &failingProjection{
		delegate:     access.NewSnapshotProjection(),
		failAccessID: accessID,
		failRevision: revision,
	}
}

func (p *failingProjection) Restore(plans []access.AccessPlanSnapshot) error {
	return p.delegate.Restore(plans)
}

func (p *failingProjection) Publish(plan access.AccessPlanSnapshot) error {
	if plan.AccessID() == p.failAccessID && plan.Revision() == p.failRevision {
		return errInjectedPublication
	}
	return p.delegate.Publish(plan)
}

func (p *failingProjection) ResolveAccess(
	accessID access.AccessID,
) (access.AccessPlanSnapshot, error) {
	return p.delegate.ResolveAccess(accessID)
}

func (p *failingProjection) ResolveClientOrigin(
	origin access.ClientOrigin,
) (access.IngressBinding, error) {
	return p.delegate.ResolveClientOrigin(origin)
}

func (p *failingProjection) ActiveClientAuthorities() ([]string, error) {
	return p.delegate.ActiveClientAuthorities()
}

func (p *failingProjection) ActiveProviderProbeTargets() (
	[]access.ProviderProbeTarget,
	error,
) {
	return p.delegate.ActiveProviderProbeTargets()
}

func (p *failingProjection) MarkUnavailable(accessID access.AccessID) {
	p.delegate.MarkUnavailable(accessID)
}

func (p *failingProjection) ProjectionHealth() access.ProjectionHealth {
	return p.delegate.ProjectionHealth()
}

func assertFailureCode(t *testing.T, err error, expected access.ReasonCode) {
	t.Helper()
	var failure *access.Failure
	if !errors.As(err, &failure) {
		t.Fatalf("error is not an Access failure: %v", err)
	}
	if failure.Code != expected {
		t.Fatalf("failure code = %q, want %q", failure.Code, expected)
	}
}
