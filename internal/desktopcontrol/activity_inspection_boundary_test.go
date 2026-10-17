package desktopcontrol_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Test-only atomic mirror publication. Production publication remains Task5.
func seedHTTPInspectionMirror(t *testing.T, current *runtimepersistence.ObservationStore, control *runtimepersistence.ControlStore, path string, refs []captureidentity.Reference) {
	t.Helper()
	snapshot, err := control.ReadInspectionSnapshot(context.Background(), refs)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := control.UsagePolicyRepository().UsagePolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var since any
	if policy.CollectingSince != nil {
		since = policy.CollectingSince.UnixMilli()
	}
	if _, err = tx.Exec(`INSERT INTO inspection_mirror_generations VALUES(1,?,?,?,?,?,?,?,?,?,?,1)`, snapshot.Stamp.LayoutID, snapshot.Stamp.ArchiveEpoch, snapshot.Stamp.VisibilityRevision, snapshot.Stamp.UsagePolicyRevision, policy.Enabled, policy.RetentionDays, since, len(snapshot.Scopes), 0, 0); err != nil {
		t.Fatal(err)
	}
	for ref, fence := range snapshot.Scopes {
		if _, err = tx.Exec(`INSERT INTO inspection_mirror_fences VALUES(1,?,?,?,?)`, ref.Kind, ref.ID, fence.Generation, fence.Deleted); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = tx.Exec(`INSERT INTO inspection_mirror_active VALUES(1,1)`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

type activityHTTPRoles struct {
	contentExpiry time.Time
	root          string
	current       *runtimepersistence.ObservationStore
	control       *runtimepersistence.ControlStore
	catalog       *runtimepersistence.InspectionCatalog
	ref           captureidentity.Reference
}

func (r activityHTTPRoles) seed(t *testing.T, id string) activity.Record {
	t.Helper()
	record := activity.Record{ID: "event-" + id, SubjectID: id, Kind: activity.KindExchangeCompleted, Status: activity.StatusSucceeded, OccurredAt: time.Now().UTC().Truncate(time.Millisecond), SourceKind: activity.SourceManualProxy, SourceDisplayName: "Manual", SourceRecognition: activity.SourceRecognitionVerified, ManualCaptureID: r.ref.ID, ConnectionID: "connection-" + id, EnvironmentID: "work", EnvironmentRevision: 1, EnvironmentDigest: strings.Repeat("a", 64), ClientEndpointID: "endpoint", ClientEndpointRevision: 1, ProtocolPlanID: "plan", ProtocolPlanRevision: 1, RouteID: "route", RouteRevision: 1, AccountID: "account", AccountRevision: 1, CredentialEpoch: 1}
	record.Conversation = &agentconversation.Ref{ProjectionID: "exchange:" + id, Kind: agentconversation.KindIsolatedExchange, Evidence: agentconversation.EvidenceUndecodedExchange}
	if r.ref.Kind == captureidentity.KindManagedRun {
		record.SourceKind = activity.SourceCaptureRun
		record.SourceDisplayName = "Codex"
		record.CaptureRunID = r.ref.ID
		record.ManualCaptureID = ""
		ref, err := agentconversation.Project(agentconversation.ProjectionInput{CaptureRunID: r.ref.ID, ExchangeID: id, SourceDisplayName: "Codex", Request: &protocolcore.Request{}})
		if err != nil {
			t.Fatal(err)
		}
		record.Conversation = &ref
	}
	scope := recording.Scope{Capture: r.ref, Fence: recording.Fence{ArchiveEpoch: 1, CaptureGeneration: 1}}
	value, err := r.current.ActivityRepository().(activity.ScopedRepository).AppendScoped(context.Background(), scope, record)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func (r activityHTTPRoles) changeVisibility(t *testing.T) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(r.root, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`UPDATE runtime_storage_state SET visibility_revision=visibility_revision+1`); err != nil {
		t.Fatal(err)
	}
}

type activityHTTPContent struct {
	exchangecontent.InspectionReader
	afterPreviews   func()
	afterAvailable  func()
	afterProjection func()
	afterPaged      func()
	afterPage       func()
}

func (r *activityHTTPContent) RequestPreviews(ctx context.Context, ids []string) (map[string]exchangecontent.RequestPreview, error) {
	value, err := r.InspectionReader.RequestPreviews(ctx, ids)
	if r.afterPreviews != nil {
		r.afterPreviews()
	}
	return value, err
}
func TestActivityHTTPInspectionKeepsOpaqueContinuation(t *testing.T) {
	fixture, _, ref, _ := captureObservationHTTPFixture(t, func(o *desktopcontrol.Options, r activityHTTPRoles) { r.seed(t, "first"); r.seed(t, "second") })
	path := "/api/v1/activities?limit=1&manualCaptureId=" + ref.ID
	first := doRequest(t, fixture.router, fixture.authority, http.MethodGet, path, fixture.readToken, nil)
	var page desktopcontrol.ActivityPage
	if first.Code != 200 || json.Unmarshal(first.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatalf("independent first page: %d %s", first.Code, first.Body)
	}
	next := doRequest(t, fixture.router, fixture.authority, http.MethodGet, path+"&cursor="+url.QueryEscape(page.NextCursor), fixture.readToken, nil)
	var tail desktopcontrol.ActivityPage
	if next.Code != 200 || json.Unmarshal(next.Body.Bytes(), &tail) != nil || len(tail.Items) != 1 || tail.Items[0].ID == page.Items[0].ID || tail.NextCursor != "" {
		t.Fatalf("independent continuation: %d %s", next.Code, next.Body)
	}
}
func TestActivityHTTPInspectionWithholdsPageChangedDuringPreviews(t *testing.T) {
	fixture, _, ref, _ := captureObservationHTTPFixture(t, func(o *desktopcontrol.Options, r activityHTTPRoles) {
		r.seed(t, "private-request")
		o.Contents = &activityHTTPContent{InspectionReader: o.Contents.(exchangecontent.InspectionReader), afterPreviews: func() { r.changeVisibility(t) }}
	})
	response := doRequest(t, fixture.router, fixture.authority, http.MethodGet, "/api/v1/activities?manualCaptureId="+ref.ID, fixture.readToken, nil)
	if response.Code != 409 || strings.Contains(response.Body.String(), "private-request") {
		t.Fatalf("stale partial Activity escaped: %d %s", response.Code, response.Body)
	}
}

func TestActivityHTTPInspectionConversationAndSearchCursors(t *testing.T) {
	for _, route := range []string{"/api/v1/conversations?limit=1", "/api/v1/evidence/search?limit=1&q=Manual"} {
		t.Run(route, func(t *testing.T) {
			fixture, _, _, _ := captureObservationHTTPFixture(t, func(o *desktopcontrol.Options, r activityHTTPRoles) { r.seed(t, "first"); r.seed(t, "second") })
			first := doRequest(t, fixture.router, fixture.authority, http.MethodGet, route, fixture.readToken, nil)
			var page struct {
				Items      []json.RawMessage `json:"items"`
				NextCursor string            `json:"nextCursor"`
			}
			if first.Code != 200 || json.Unmarshal(first.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.NextCursor == "" {
				t.Fatalf("first page: %d %s", first.Code, first.Body)
			}
			next := doRequest(t, fixture.router, fixture.authority, http.MethodGet, route+"&cursor="+url.QueryEscape(page.NextCursor), fixture.readToken, nil)
			var tail struct {
				Items      []json.RawMessage `json:"items"`
				NextCursor string            `json:"nextCursor"`
			}
			if next.Code != 200 || json.Unmarshal(next.Body.Bytes(), &tail) != nil || len(tail.Items) != 1 || string(tail.Items[0]) == string(page.Items[0]) || tail.NextCursor != "" {
				t.Fatalf("next page: %d %s", next.Code, next.Body)
			}
		})
	}
}

func (r activityHTTPRoles) seedContent(t *testing.T, record activity.Record, expires time.Time) exchangecontent.Record {
	t.Helper()
	block, err := protocolcore.NewTextBlock("retained private body")
	if err != nil {
		t.Fatal(err)
	}
	content, err := exchangecontent.NewRecord(record.SubjectID, exchangecontent.FrozenRef{EnvironmentID: record.EnvironmentID, EnvironmentRevision: record.EnvironmentRevision, EnvironmentDigest: record.EnvironmentDigest, ClientEndpointID: record.ClientEndpointID, ClientEndpointRevision: record.ClientEndpointRevision, ProtocolPlanID: record.ProtocolPlanID, ProtocolPlanRevision: record.ProtocolPlanRevision, RouteID: record.RouteID, RouteRevision: record.RouteRevision}, environment.DefaultContentRecordingPolicy(), record.OccurredAt, protocolcore.Request{RequestedModel: "model", EffectiveModel: "model", MaxOutputTokens: 10, Messages: []protocolcore.Message{{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{block}}}}, nil, exchangecontent.WithParentRef(exchangecontent.ParentRef{ManualCaptureID: r.ref.ID}))
	if err != nil {
		t.Fatal(err)
	}
	content.ExpiresAt = expires
	limits, err := exchangecontent.DefaultSourceLimits()
	if err != nil {
		t.Fatal(err)
	}
	source, err := exchangecontent.SourceFromRecordWithin(limits, content)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.current.ExchangeContentRepository().(exchangecontent.ScopedSourceRepository).PutScopedSource(context.Background(), recording.Scope{Capture: r.ref, Fence: recording.Fence{ArchiveEpoch: 1, CaptureGeneration: 1}}, source); err != nil {
		t.Fatal(err)
	}
	return content
}
func (r *activityHTTPContent) InspectAvailableBodies(ctx context.Context, ids []string) (exchangecontent.BodyAvailability, error) {
	value, err := r.InspectionReader.InspectAvailableBodies(ctx, ids)
	if r.afterAvailable != nil {
		r.afterAvailable()
	}
	return value, err
}
func (r *activityHTTPContent) GetProjection(ctx context.Context, id string, view exchangecontent.RequestView) (exchangecontent.Projection, error) {
	value, err := r.InspectionReader.GetProjection(ctx, id, view)
	if r.afterProjection != nil {
		r.afterProjection()
	}
	return value, err
}
func TestActivityHTTPInspectionOriginalExpirySurvivesEnrichment(t *testing.T) {
	for _, route := range []string{"activities", "search", "conversations", "detail"} {
		t.Run(route, func(t *testing.T) {
			fixture, _, ref, _ := captureObservationHTTPFixture(t, func(o *desktopcontrol.Options, r activityHTTPRoles) {
				record := r.seed(t, "expires-during-enrichment")
				content := r.seedContent(t, record, time.Now().UTC().Truncate(time.Millisecond).Add(2*time.Second))
				delayed := &activityHTTPContent{InspectionReader: o.Contents.(exchangecontent.InspectionReader)}
				wait := func() { time.Sleep(time.Until(content.ExpiresAt) + time.Millisecond) }
				switch route {
				case "conversations":
					delayed.afterAvailable = wait
				case "detail":
					delayed.afterProjection = wait
				default:
					delayed.afterPreviews = wait
				}
				o.Contents = delayed
			})
			path := "/api/v1/activities?manualCaptureId=" + ref.ID
			switch route {
			case "search":
				path = "/api/v1/evidence/search?q=Manual"
			case "conversations":
				path = "/api/v1/conversations?manualCaptureId=" + ref.ID
			case "detail":
				path = "/api/v1/exchanges/expires-during-enrichment"
			}
			response := doRequest(t, fixture.router, fixture.authority, http.MethodGet, path, fixture.readToken, nil)
			if response.Code != 409 || strings.Contains(response.Body.String(), "retained private body") || strings.Contains(response.Body.String(), "expires-during-enrichment") {
				t.Fatalf("expired partial response: %d %s", response.Code, response.Body)
			}
		})
	}
}

func (r activityHTTPRoles) managed(t *testing.T) activityHTTPRoles {
	t.Helper()
	r.ref = captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "run-http"}
	for _, spec := range []struct{ path, statement string }{
		{"control.db", `INSERT INTO recording_capture_fences(capture_kind,capture_id,generation,deleted) VALUES('managed_run','run-http',1,0)`},
		{"observations.db", `INSERT INTO inspection_mirror_fences VALUES(1,'managed_run','run-http',1,0)`},
		{"observations.db", `UPDATE inspection_mirror_generations SET fence_count=fence_count+1`},
	} {
		db, err := sql.Open("sqlite", filepath.Join(r.root, spec.path))
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.Exec(spec.statement)
		db.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	return r
}
func (r activityHTTPRoles) toolContent(t *testing.T, record activity.Record, messages []protocolcore.Message) {
	t.Helper()
	content, err := exchangecontent.NewRecord(record.SubjectID, exchangecontent.FrozenRef{EnvironmentID: record.EnvironmentID, EnvironmentRevision: record.EnvironmentRevision, EnvironmentDigest: record.EnvironmentDigest, ClientEndpointID: record.ClientEndpointID, ClientEndpointRevision: record.ClientEndpointRevision, ProtocolPlanID: record.ProtocolPlanID, ProtocolPlanRevision: record.ProtocolPlanRevision, RouteID: record.RouteID, RouteRevision: record.RouteRevision}, environment.DefaultContentRecordingPolicy(), record.OccurredAt, protocolcore.Request{RequestedModel: "model", EffectiveModel: "model", MaxOutputTokens: 10, Messages: messages}, nil, exchangecontent.WithParentRef(exchangecontent.ParentRef{CaptureRunID: r.ref.ID}))
	if err != nil {
		t.Fatal(err)
	}
	if !r.contentExpiry.IsZero() {
		content.ExpiresAt = r.contentExpiry
	}
	limits, err := exchangecontent.DefaultSourceLimits()
	if err != nil {
		t.Fatal(err)
	}
	source, err := exchangecontent.SourceFromRecordWithin(limits, content)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.current.ExchangeContentRepository().(exchangecontent.ScopedSourceRepository).PutScopedSource(context.Background(), recording.Scope{Capture: r.ref, Fence: recording.Fence{ArchiveEpoch: 1, CaptureGeneration: 1}}, source); err != nil {
		t.Fatal(err)
	}
}
func toolHTTPInspectionFixture(t *testing.T, configure ...func(*desktopcontrol.Options, activityHTTPRoles)) *auditFixture {
	return toolHTTPInspectionFixtureWithin(t, 0, configure...)
}
func toolHTTPInspectionFixtureWithin(t *testing.T, lifetime time.Duration, configure ...func(*desktopcontrol.Options, activityHTTPRoles)) *auditFixture {
	fixture, _, _, _ := captureObservationHTTPFixture(t, func(o *desktopcontrol.Options, r activityHTTPRoles) {
		r = r.managed(t)
		if lifetime > 0 {
			r.contentExpiry = time.Now().UTC().Truncate(time.Millisecond).Add(lifetime)
		}
		origin := r.seed(t, "tool-origin")
		matching := r.seed(t, "tool-matching")
		middle := r.seed(t, "tool-middle")
		deferred := r.seed(t, "tool-deferred")
		text, err := protocolcore.NewTextBlock("unrelated request")
		if err != nil {
			t.Fatal(err)
		}
		messages := []protocolcore.Message{{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{text}}}
		r.toolContent(t, origin, messages)
		r.toolContent(t, middle, messages)
		key, err := protocolcore.NewCallKey("test", "wanted")
		if err != nil {
			t.Fatal(err)
		}
		result, err := protocolcore.NewToolResultBlock(protocolcore.ToolResult{Key: key, Content: "ACTUAL_MATCH"})
		if err != nil {
			t.Fatal(err)
		}
		r.toolContent(t, matching, append(messages, protocolcore.Message{Role: protocolcore.RoleTool, Blocks: []protocolcore.ContentBlock{result}}))
		for range 60 {
			messages = append(messages, protocolcore.Message{Role: protocolcore.RoleAssistant, Blocks: []protocolcore.ContentBlock{text}})
		}
		r.toolContent(t, deferred, messages)
		for _, setup := range configure {
			setup(o, r)
		}
	})
	return fixture
}
func TestActivityHTTPInspectionToolResumeKeepsRemainingCandidates(t *testing.T) {
	fixture := toolHTTPInspectionFixture(t)
	cursor := ""
	for round := 0; round < 12; round++ {
		path := "/api/v1/exchanges/tool-origin/tool-results?callId=wanted"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		response := doRequest(t, fixture.router, fixture.authority, http.MethodGet, path, fixture.readToken, nil)
		var page struct {
			Items []struct {
				ExchangeID string `json:"exchangeId"`
			} `json:"items"`
			Next string `json:"nextCursor"`
		}
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &page) != nil {
			t.Fatalf("round%d: %d %s", round, response.Code, response.Body)
		}
		if len(page.Items) > 0 {
			if round == 0 || page.Items[0].ExchangeID != "tool-matching" {
				t.Fatalf("wrong resumed candidate: %s", response.Body)
			}
			return
		}
		if page.Next == "" || page.Next == cursor {
			t.Fatalf("lost remaining candidates round%d: %s", round, response.Body)
		}
		cursor = page.Next
	}
	t.Fatal("tool lookup failed to reach matching candidate")
}

func (r *activityHTTPContent) GetPagedProjection(ctx context.Context, id string, view exchangecontent.RequestView) (exchangecontent.Projection, error) {
	value, err := r.InspectionReader.GetPagedProjection(ctx, id, view)
	if r.afterPaged != nil {
		r.afterPaged()
	}
	return value, err
}
func (r *activityHTTPContent) GetContentPage(ctx context.Context, id, cursor string) (exchangecontent.ContentPage, error) {
	value, err := r.InspectionReader.GetContentPage(ctx, id, cursor)
	if r.afterPage != nil {
		r.afterPage()
	}
	return value, err
}
func TestActivityHTTPInspectionToolChecksBothPublicationExits(t *testing.T) {
	for _, resumed := range []bool{false, true} {
		t.Run(fmt.Sprint(resumed), func(t *testing.T) {
			var hook *activityHTTPContent
			var mutate func()
			fixture := toolHTTPInspectionFixture(t, func(o *desktopcontrol.Options, r activityHTTPRoles) {
				hook = &activityHTTPContent{InspectionReader: o.Contents.(exchangecontent.InspectionReader)}
				o.Contents = hook
				mutate = func() { r.changeVisibility(t) }
			})
			path := "/api/v1/exchanges/tool-origin/tool-results?callId=wanted"
			if resumed {
				response := doRequest(t, fixture.router, fixture.authority, http.MethodGet, path, fixture.readToken, nil)
				var page struct {
					Next string `json:"nextCursor"`
				}
				if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &page) != nil || page.Next == "" {
					t.Fatalf("first tool page: %d %s", response.Code, response.Body)
				}
				path += "&cursor=" + url.QueryEscape(page.Next)
				hook.afterPage = mutate
			} else {
				hook.afterPaged = mutate
			}
			response := doRequest(t, fixture.router, fixture.authority, http.MethodGet, path, fixture.readToken, nil)
			if response.Code != 409 || strings.Contains(response.Body.String(), "nextCursor") || strings.Contains(response.Body.String(), "tool-origin") {
				t.Fatalf("stale tool response: %d %s", response.Code, response.Body)
			}
		})
	}
}
func TestActivityHTTPInspectionToolCursorRefusesSourceGenerationChange(t *testing.T) {
	var roles activityHTTPRoles
	fixture := toolHTTPInspectionFixture(t, func(_ *desktopcontrol.Options, r activityHTTPRoles) { roles = r })
	path := "/api/v1/exchanges/tool-origin/tool-results?callId=wanted"
	first := doRequest(t, fixture.router, fixture.authority, http.MethodGet, path, fixture.readToken, nil)
	var page struct {
		Next string `json:"nextCursor"`
	}
	if first.Code != 200 || json.Unmarshal(first.Body.Bytes(), &page) != nil || page.Next == "" {
		t.Fatalf("first tool page: %d %s", first.Code, first.Body)
	}
	maintenance, err := roles.catalog.BeginMaintenance(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	maintenance.Close()
	next := doRequest(t, fixture.router, fixture.authority, http.MethodGet, path+"&cursor="+url.QueryEscape(page.Next), fixture.readToken, nil)
	if next.Code != 409 && next.Code != 422 {
		t.Fatalf("old nested/source cursor accepted: %d %s", next.Code, next.Body)
	}
}
func TestActivityHTTPInspectionMissingSnapshotAndContentOnlyStayDistinct(t *testing.T) {
	var roles activityHTTPRoles
	fixture, _, _, _ := captureObservationHTTPFixture(t, func(_ *desktopcontrol.Options, r activityHTTPRoles) {
		roles = r
		r.seed(t, "no-snapshot")
		record := r.seed(t, "content-only")
		r.seedContent(t, record, time.Now().Add(time.Hour))
	})
	db, err := sql.Open("sqlite", filepath.Join(roles.root, "observations.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DELETE FROM runtime_activities WHERE subject_id='content-only'`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	missing := doRequest(t, fixture.router, fixture.authority, http.MethodGet, "/api/v1/exchanges/no-snapshot", fixture.readToken, nil)
	if missing.Code != 200 || !strings.Contains(missing.Body.String(), `"state":"not_recorded"`) {
		t.Fatalf("request without snapshot: %d %s", missing.Code, missing.Body)
	}
	orphan := doRequest(t, fixture.router, fixture.authority, http.MethodGet, "/api/v1/exchanges/content-only", fixture.readToken, nil)
	if orphan.Code != 404 || strings.Contains(orphan.Body.String(), "retained private body") {
		t.Fatalf("content-only fabricated Activity: %d %s", orphan.Code, orphan.Body)
	}
}

func TestActivityHTTPInspectionToolExpirySurvivesNestedHydration(t *testing.T) {
	for _, resumed := range []bool{false, true} {
		t.Run(fmt.Sprint(resumed), func(t *testing.T) {
			var hook *activityHTTPContent
			var wait func()
			fixture := toolHTTPInspectionFixtureWithin(t, 3*time.Second, func(o *desktopcontrol.Options, r activityHTTPRoles) {
				hook = &activityHTTPContent{InspectionReader: o.Contents.(exchangecontent.InspectionReader)}
				o.Contents = hook
				wait = func() { time.Sleep(time.Until(r.contentExpiry) + time.Millisecond) }
			})
			path := "/api/v1/exchanges/tool-origin/tool-results?callId=wanted"
			if resumed {
				response := doRequest(t, fixture.router, fixture.authority, http.MethodGet, path, fixture.readToken, nil)
				var page struct {
					Next string `json:"nextCursor"`
				}
				if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &page) != nil || page.Next == "" {
					t.Fatalf("first tool page: %d %s", response.Code, response.Body)
				}
				path += "&cursor=" + url.QueryEscape(page.Next)
				hook.afterPage = wait
			} else {
				hook.afterPaged = wait
			}
			response := doRequest(t, fixture.router, fixture.authority, http.MethodGet, path, fixture.readToken, nil)
			if response.Code != 409 || strings.Contains(response.Body.String(), "nextCursor") || strings.Contains(response.Body.String(), "tool-origin") {
				t.Fatalf("expired nested tool response: %d %s", response.Code, response.Body)
			}
		})
	}
}

func TestActivityHTTPInspectionRequiresOneConstituentSourceVersion(t *testing.T) {
	fixture, _, ref, _ := captureObservationHTTPFixture(t, func(o *desktopcontrol.Options, r activityHTTPRoles) {
		r.seed(t, "mixed-view")
		catalog, err := runtimepersistence.NewInspectionCatalog(runtimepersistence.InspectionCatalogOptions{Root: r.root, LayoutID: "capture-http", Generation: 2, Sources: []runtimepersistence.InspectionSource{{ID: "current", RelativePath: "observations.db", Database: runtimepersistence.SchemaState{Identity: "vibermate-observations", Revision: 1}, Current: true}}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { catalog.Close(context.Background()) })
		limits, err := exchangecontent.DefaultSourceLimits()
		if err != nil {
			t.Fatal(err)
		}
		o.Contents, err = runtimepersistence.NewContentInspectionReader(catalog, r.control, limits)
		if err != nil {
			t.Fatal(err)
		}
	})
	response := doRequest(t, fixture.router, fixture.authority, http.MethodGet, "/api/v1/activities?manualCaptureId="+ref.ID, fixture.readToken, nil)
	if response.Code != 409 || strings.Contains(response.Body.String(), "mixed-view") {
		t.Fatalf("mixed independent source views escaped: %d %s", response.Code, response.Body)
	}
}

func TestActivityHTTPInspectionEmptySchedulerWatermarkBindsSourceVersion(t *testing.T) {
	clock := &scheduleClock{now: time.Now().UTC()}
	indexer := &countingIndexer{caughtUp: true}
	var roles activityHTTPRoles
	fixture, _, _, _ := captureObservationHTTPFixture(t, func(o *desktopcontrol.Options, r activityHTTPRoles) {
		roles = r.managed(t)
		o.ConversationIndexer = indexer
		o.Clock = clock
	})
	get := func() {
		t.Helper()
		response := doRequest(t, fixture.router, fixture.authority, http.MethodGet, "/api/v1/conversations?captureRunId=run-http", fixture.readToken, nil)
		if response.Code != 200 {
			t.Fatalf("directory: %d %s", response.Code, response.Body)
		}
	}
	get()
	waitFor(t, func() bool { return indexer.reindexes.Load() == 1 }, "first empty source scan")
	maintenance, err := roles.catalog.BeginMaintenance(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	maintenance.Close()
	clock.advance(6 * time.Second)
	get()
	waitFor(t, func() bool { return indexer.reindexes.Load() == 2 }, "empty source-version scan")
}
