package runtimepersistence

import (
	"context"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

type nativeLeaseActivity struct {
	reader manualcapture.ActivityReader
	clock  *manualCaptureClock
}

func (activity nativeLeaseActivity) Active(ctx context.Context, capture captureidentity.Reference) (bool, error) {
	id, err := manualcapture.ParseID(capture.ID)
	if err != nil || capture.Kind != captureidentity.KindManualCapture {
		return false, err
	}
	return activity.reader.Active(ctx, id, activity.clock.Now())
}

type nativeLeaseFixture struct {
	store        *Store
	assignments  *captureassignment.Manager
	environments *environment.Manager
	capture      captureidentity.Reference
	connectionID string
	facts        environment.RequestFacts
	snapshot     environment.EnvironmentSnapshot
	clock        *manualCaptureClock
	admission    captureadmission.Admission
}

func newNativeLeaseFixture(t *testing.T, enabled, shared bool, nativeMode ...environment.ToolPolicyMode) nativeLeaseFixture {
	t.Helper()
	ctx := context.Background()
	store := openTestStore(t, t.TempDir()+"/runtime.db")
	t.Cleanup(func() { shutdownTestStore(t, store) })
	environments := newEnvironmentManager(t, store)
	candidate := environmentFixture(t, "native.lease", 1)
	if len(nativeMode) > 0 {
		candidate.PolicySet = &environment.PolicySet{ToolMode: nativeMode[0]}
	}
	origin := runtimeProviderOrigin(t, "https://native.example")
	if shared {
		origin = runtimeProviderOrigin(t, "https://relay.example")
	}
	if enabled {
		candidate.ClientToolAccess = &environment.ClientToolAccessPolicy{Enabled: true, Route: &environment.ClientToolRoute{
			ID: "native.lease.route", Revision: 1, TargetOrigin: origin, Path: environment.ClientToolMCPPath,
			CredentialOwner: environment.ClientToolCredentialOriginal, Contract: environment.ClientToolContractCodexLegacy, EgressProfile: egressprofile.Direct(),
		}}
	}
	publishEnvironmentRevision(t, environments, candidate, 0)
	snapshot, err := environments.Resolve(candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	clock := &manualCaptureClock{now: time.Now().UTC().Truncate(time.Millisecond)}
	manuals := newManualCaptureManager(t, store, clock, &manualCaptureRandom{})
	t.Cleanup(func() {
		if err := manuals.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	grant, err := manuals.Create(ctx, manualcapture.CreateCommand{Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "Synthetic native lease", ClientClass: manualcapture.ClientDesktopApp, Lifetime: manualcapture.LifetimeTemporary, ExpiresIn: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	runs, err := capturerun.NewManager(ctx, capturerun.DefaultOptions(store.CaptureRunRepository()))
	if err != nil {
		t.Fatal(err)
	}
	authorizer, err := captureadmission.NewAuthorizer(runs, manuals)
	t.Cleanup(func() {
		if err := runs.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	credential, err := captureadmission.NewProxyCredential(grant.Credential.Value())
	if err != nil {
		t.Fatal(err)
	}
	admission, err := authorizer.Authorize(ctx, credential)
	if err != nil {
		t.Fatal(err)
	}
	capture, err := admission.CaptureReference()
	if err != nil {
		t.Fatal(err)
	}
	assignments, err := captureassignment.NewManager(captureassignment.Options{Repository: store.CaptureAssignmentRepository(), Environments: environments, Activity: nativeLeaseActivity{store.ManualCaptureRepository(), clock}, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := assignments.Create(ctx, captureassignment.CreateCommand{Capture: capture, EnvironmentID: candidate.ID, Source: captureassignment.SourceManualCreate}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := assignments.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	connection, err := assignments.RegisterProviderConnection(ctx, capture, "native.connection", origin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(connection.Close)
	return nativeLeaseFixture{store: store, assignments: assignments, environments: environments, capture: capture, connectionID: "native.connection", snapshot: snapshot, clock: clock, admission: admission, facts: environment.RequestFacts{Target: protocolspec.RequestTarget{Method: "POST", Path: environment.ClientToolMCPPath, Transport: protocolspec.ClientOperationTransportHTTP}, DownstreamProtocol: wireprofile.ApplicationProtocolHTTP1}}
}

func TestNativeLeaseExactAdmissionAndLiveCapture(t *testing.T) {
	fixture := newNativeLeaseFixture(t, true, false)
	ctx := context.Background()
	for _, method := range []string{"GET", "DELETE", "POST"} {
		facts := fixture.facts
		facts.Target.Method = method
		lease, err := fixture.assignments.BeginRequest(ctx, fixture.capture, fixture.connectionID, facts)
		if err != nil {
			t.Fatal(err)
		}
		lease.Release()
		lease.Release()
	}
	for _, mutate := range []func(*environment.RequestFacts){
		func(f *environment.RequestFacts) { f.Target.Path += "/" },
		func(f *environment.RequestFacts) { f.Target.RawQuery = "x=1" },
		func(f *environment.RequestFacts) { f.Target.RawPath = f.Target.Path },
		func(f *environment.RequestFacts) { f.Target.Method = "PUT" },
		func(f *environment.RequestFacts) { f.Target.Transport = protocolspec.ClientOperationTransportWebSocket },
	} {
		facts := fixture.facts
		mutate(&facts)
		if lease, err := fixture.assignments.BeginRequest(ctx, fixture.capture, fixture.connectionID, facts); err == nil {
			lease.Release()
			t.Fatal("nonexact native request accepted")
		}
	}
	wrong, err := fixture.assignments.RegisterProviderConnection(ctx, fixture.capture, "wrong.origin", runtimeProviderOrigin(t, "https://native.example:8443"))
	if err != nil {
		t.Fatal(err)
	}
	defer wrong.Close()
	if lease, err := fixture.assignments.BeginRequest(ctx, fixture.capture, "wrong.origin", fixture.facts); err == nil {
		lease.Release()
		t.Fatal("wrong native origin accepted")
	}
	fixture.clock.Set(fixture.clock.Now().Add(2 * time.Minute))
	if lease, err := fixture.assignments.BeginRequest(ctx, fixture.capture, fixture.connectionID, fixture.facts); err == nil {
		lease.Release()
		t.Fatal("expired Capture admitted")
	}
}

func TestNativeLeaseSharedHostKeepsModelAndNativeSeparate(t *testing.T) {
	fixture := newNativeLeaseFixture(t, true, true)
	native, err := fixture.assignments.BeginRequest(context.Background(), fixture.capture, fixture.connectionID, fixture.facts)
	if err != nil {
		t.Fatal(err)
	}
	defer native.Release()
	facts := fixture.facts
	facts.Target.Path = "/v1/messages"
	model, err := fixture.assignments.BeginRequest(context.Background(), fixture.capture, fixture.connectionID, facts)
	if err != nil {
		t.Fatal(err)
	}
	defer model.Release()
	if _, ok := model.Plan().ClientToolPlan(); ok {
		t.Fatal("model acquired native arm")
	}
	route, ok := model.Plan().UpstreamRoute()
	account, fixed := route.AccountPolicy().FixedAccount()
	if !ok || route.ID() != "route.anthropic" || !fixed || account.ID != "account.runtime" {
		t.Fatal("model lost selected managed account")
	}
	if _, ok := native.Plan().UpstreamRoute(); ok {
		t.Fatal("native acquired model account")
	}
}

func TestNativeLeaseApplyLatestCannotGainLaunchAuthority(t *testing.T) {
	for _, change := range []string{"initial_off", "route", "profile", "same"} {
		t.Run(change, func(t *testing.T) {
			fixture := newNativeLeaseFixture(t, change != "initial_off", true)
			var inFlight *captureassignment.RequestLease
			if change == "initial_off" {
				if lease, err := fixture.assignments.BeginRequest(context.Background(), fixture.capture, fixture.connectionID, fixture.facts); err == nil {
					lease.Release()
					t.Fatal("OFF admitted native")
				}
			} else {
				var err error
				inFlight, err = fixture.assignments.BeginRequest(context.Background(), fixture.capture, fixture.connectionID, fixture.facts)
				if err != nil {
					t.Fatal(err)
				}
				defer inFlight.Release()
			}
			next := fixture.snapshot.Aggregate()
			next.Revision++
			if change == "initial_off" {
				next.ClientToolAccess = &environment.ClientToolAccessPolicy{Enabled: true, Route: &environment.ClientToolRoute{ID: "native.new", Revision: 1, TargetOrigin: runtimeProviderOrigin(t, "https://relay.example"), Path: environment.ClientToolMCPPath, CredentialOwner: environment.ClientToolCredentialOriginal, Contract: environment.ClientToolContractCodexLegacy, EgressProfile: egressprofile.Direct()}}
			} else if change == "route" {
				next.ClientToolAccess.Route.ID = "native.new"
			} else if change == "profile" {
				next.ClientToolAccess.Route.Revision++
				next.ClientToolAccess.Route.EgressProfile.DisplayName = "Changed profile"
			}
			publishEnvironmentRevision(t, fixture.environments, next, 1)
			applied, err := fixture.assignments.ApplyLatest(context.Background(), fixture.capture, 1)
			if err != nil {
				t.Fatal(err)
			}
			if inFlight != nil && (inFlight.Plan().EnvironmentRevision() != 1 || inFlight.Plan().EnvironmentDigest() != fixture.snapshot.Digest() || inFlight.Assignment().Revision != 1) {
				t.Fatal("ApplyLatest mutated in-flight native lease")
			}
			lease, err := fixture.assignments.BeginRequest(context.Background(), fixture.capture, fixture.connectionID, fixture.facts)
			if change != "same" {
				if err == nil {
					lease.Release()
					t.Fatal("ApplyLatest gained native authority")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Release()
			if lease.Assignment() != applied || lease.Plan().EnvironmentRevision() != 2 {
				t.Fatal("unchanged native route did not use frozen applied assignment")
			}
		})
	}
}

func TestNativeOnlyLeaseApplyLatestKeepsOriginalRouteAuthority(t *testing.T) {
	fixture := newNativeLeaseFixture(t, true, false)
	initial := fixture.snapshot
	old, err := fixture.assignments.BeginRequest(context.Background(), fixture.capture, fixture.connectionID, fixture.facts)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Release()
	next := initial.Aggregate()
	next.Revision++
	next.Name = "Updated native-only policy"
	publishEnvironmentRevision(t, fixture.environments, next, 1)
	applied, err := fixture.assignments.ApplyLatest(context.Background(), fixture.capture, 1)
	if err != nil {
		t.Fatal(err)
	}
	current, err := fixture.assignments.BeginRequest(context.Background(), fixture.capture, fixture.connectionID, fixture.facts)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Release()
	native, ok := current.Plan().ClientToolPlan()
	if !ok || current.Assignment() != applied || native.EnvironmentRevision() != 2 || native.Route() != *initial.Aggregate().ClientToolAccess.Route || applied.LaunchAuthority != old.Assignment().LaunchAuthority || old.Plan().EnvironmentRevision() != 1 {
		t.Fatal("native-only apply changed original launch route authority")
	}
	if _, model := current.Plan().UpstreamRoute(); model {
		t.Fatal("native-only apply fabricated model authority")
	}
}

func TestNativeLeaseRejectsMissingOrSubstitutedHistoricalSnapshot(t *testing.T) {
	for _, corruption := range []string{"missing", "digest", "off"} {
		t.Run(corruption, func(t *testing.T) {
			fixture := newNativeLeaseFixture(t, true, true)
			next := fixture.snapshot.Aggregate()
			next.Revision++
			next.Name = "Updated policy"
			publishEnvironmentRevision(t, fixture.environments, next, 1)
			if _, err := fixture.assignments.ApplyLatest(context.Background(), fixture.capture, 1); err != nil {
				t.Fatal(err)
			}
			// Corrupt only isolated historical SQLite evidence after the live assignment advances.
			query := `DELETE FROM environment_revisions WHERE environment_id=? AND revision=1`
			args := []any{fixture.snapshot.ID()}
			if corruption == "digest" {
				query = `UPDATE environment_revisions SET candidate_digest=zeroblob(32) WHERE environment_id=? AND revision=1`
			}
			if corruption == "off" {
				off := fixture.snapshot.Aggregate()
				off.ClientToolAccess = nil
				encoded, err := environment.CanonicalJSON(off)
				if err != nil {
					t.Fatal(err)
				}
				digest, err := environment.Digest(off)
				if err != nil {
					t.Fatal(err)
				}
				query = `UPDATE environment_revisions SET payload_json=?,candidate_digest=? WHERE environment_id=? AND revision=1`
				args = []any{encoded, digest[:], fixture.snapshot.ID()}
			}
			if _, err := fixture.store.database.ExecContext(context.Background(), query, args...); err != nil {
				t.Fatal(err)
			}
			if lease, err := fixture.assignments.BeginRequest(context.Background(), fixture.capture, fixture.connectionID, fixture.facts); err == nil {
				lease.Release()
				t.Fatal("unproven initial snapshot accepted")
			}
		})
	}
}

func TestNativeBeginRequestUsesRealPublishedAssignment(t *testing.T) {
	fixture := newNativeLeaseFixture(t, true, false)
	lease, err := fixture.assignments.BeginRequest(context.Background(), fixture.capture, fixture.connectionID, fixture.facts)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	native, ok := lease.Plan().ClientToolPlan()
	if !ok || native.EnvironmentID() != fixture.snapshot.ID() || native.EnvironmentDigest() != fixture.snapshot.Digest() {
		t.Fatal("wrong frozen native plan")
	}
	if _, model := lease.Plan().UpstreamRoute(); model {
		t.Fatal("native lease fabricated model routing")
	}
}
