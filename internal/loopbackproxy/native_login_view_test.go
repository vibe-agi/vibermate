package loopbackproxy_test

import (
	"context"
	"encoding/json"
	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/capturegrant"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/clienttarget"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"strings"
	"testing"
	"time"
)

func TestNativeLaunchViewUsesVerifiedInitialSnapshotForCreateAndResolve(t *testing.T) {
	ctx := context.Background()
	for _, scenario := range []string{"on", "off", "missing", "substituted", "old_off_after_on"} {
		t.Run(scenario, func(t *testing.T) {
			off := scenario == "off" || scenario == "old_off_after_on"
			f := newNativeFixtureMode(t, environment.ToolPolicyReview, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, &nativeHandlerSettings{shared: off, off: off})
			resolver, err := capturegrant.NewEnvironmentAuthorityResolver(f.assignments, f.environments)
			if err != nil {
				t.Fatal(err)
			}
			grant, err := f.manuals.Create(ctx, manualcapture.CreateCommand{Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "Synthetic launch metadata", ClientClass: manualcapture.ClientDesktopApp, Lifetime: manualcapture.LifetimeTemporary, ExpiresIn: time.Hour})
			if err != nil {
				t.Fatal(err)
			}
			capture, _ := captureidentity.New(captureidentity.KindManualCapture, grant.Capture.ID)
			created, err := resolver.AssignAndResolve(ctx, capture, "native.synthetic", captureassignment.SourceManualCreate, clienttarget.Profile{})
			if err != nil {
				t.Fatal(err)
			}
			if off != (created.NativeToolLogin() == nil) {
				t.Fatal("create native login metadata did not follow frozen policy")
			}
			if scenario == "old_off_after_on" {
				snapshot, err := f.environments.Resolve("native.synthetic")
				if err != nil {
					t.Fatal(err)
				}
				next := snapshot.Aggregate()
				next.Revision++
				origin, _ := originidentity.ParseProviderOrigin("https://native.example")
				next.ClientToolAccess = &environment.ClientToolAccessPolicy{Enabled: true, Route: &environment.ClientToolRoute{ID: "native.new.route", Revision: 1, TargetOrigin: origin, Path: environment.ClientToolMCPPath, CredentialOwner: environment.ClientToolCredentialOriginal, Contract: environment.ClientToolContractCodexLegacy, EgressProfile: authoredEgress(t, egressprofile.Direct())}}
				draft, err := f.environments.SaveDraft(ctx, environment.DraftCommand{Candidate: next, ExpectedBaseRevision: 1})
				if err != nil {
					t.Fatal(err)
				}
				preview, err := f.environments.Preview(ctx, next.ID, draft.Revision)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = f.environments.Publish(ctx, preview); err != nil {
					t.Fatal(err)
				}
				if _, err = f.assignments.ApplyLatest(ctx, capture, 1); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "missing" {
				if _, err := f.db.Exec(`DELETE FROM environment_revisions WHERE environment_id='native.synthetic' AND revision=1`); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "substituted" {
				if _, err := f.db.Exec(`UPDATE environment_revisions SET candidate_digest=zeroblob(32) WHERE environment_id='native.synthetic' AND revision=1`); err != nil {
					t.Fatal(err)
				}
			}
			resolved, err := resolver.Resolve(ctx, capture)
			if scenario == "missing" || scenario == "substituted" {
				if err == nil {
					t.Fatal("unverified original snapshot produced native login metadata")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if off != (resolved.NativeToolLogin() == nil) {
				t.Fatal("resolve native metadata followed latest permission")
			}
			if !off {
				view := resolved.NativeToolLogin()
				if view.Origin != "https://native.example" || view.ServicePath != "/backend-api/ps/mcp" || view.Contract != "codex_legacy_2025_06_18" {
					t.Fatalf("view %+v", view)
				}
				encoded, _ := json.Marshal(view)
				if strings.Contains(string(encoded), "token") || strings.Contains(string(encoded), "account") || strings.Contains(string(encoded), "grant") {
					t.Fatal("native login view carried authorization data")
				}
			}
		})
	}
}
