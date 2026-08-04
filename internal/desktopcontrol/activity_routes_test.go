package desktopcontrol_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/productruntime"
)

func TestActivityRouteProjectsExchangePagesWithoutWireLeakage(t *testing.T) {
	t.Parallel()

	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	router, authority, readToken := newActivityRouteFixture(t, runtime)
	accessID, err := access.NewAccessID("activity-route-access")
	if err != nil {
		t.Fatal(err)
	}
	record := func(kind activity.Kind, subject string, status activity.Status) activity.Record {
		t.Helper()
		event := activity.Event{
			Kind:       kind,
			AccessID:   accessID,
			SubjectID:  subject,
			Status:     status,
			ReasonCode: "private_failure_reason",
		}
		if kind == activity.KindExchangeCompleted {
			profile := activity.TransportProfileEvidence{
				Ref:      "standard-strict-h1",
				Revision: 1,
				Source:   "standard",
			}
			event.Diagnosis = activity.Diagnosis{
				ProviderStatus: 502,
				ProviderField:  "messages",
			}
			event.Transport = &activity.TransportEvidence{
				Requested:     profile,
				Effective:     &profile,
				FallbackChain: []activity.TransportProfileEvidence{profile},
				HTTPTransport: "http1",
				ClientOfferedALPN: []string{
					"http/1.1",
				},
				UpstreamOfferedALPN: []string{"http/1.1"},
			}
		}
		recorded, err := runtime.Activities().Record(
			context.Background(),
			event,
		)
		if err != nil {
			t.Fatal(err)
		}
		return recorded
	}

	oldest := record(
		activity.KindExchangeCompleted,
		"exchange-route-oldest",
		activity.StatusSucceeded,
	)
	record(activity.KindAccessApplied, "access-revision-1", activity.StatusSucceeded)
	middle := record(
		activity.KindExchangeCompleted,
		"exchange-route-middle",
		activity.StatusFailed,
	)
	record(activity.KindApprovalResolved, "approval-route-1", activity.StatusFailed)
	newest := record(
		activity.KindExchangeCompleted,
		"exchange-route-newest",
		activity.StatusCanceled,
	)

	firstResponse := doRequest(
		t,
		router,
		authority,
		http.MethodGet,
		"/api/v1/activities?limit=2",
		readToken,
		nil,
	)
	if firstResponse.Code != http.StatusOK {
		t.Fatalf(
			"first Activity page status=%d body=%s",
			firstResponse.Code,
			firstResponse.Body,
		)
	}
	firstPayload := append([]byte(nil), firstResponse.Body.Bytes()...)
	var first desktopcontrol.ActivityPage
	decodeResponse(t, firstResponse, &first)
	if len(first.Items) != 2 ||
		first.Items[0].ID != newest.SubjectID ||
		first.Items[1].ID != middle.SubjectID ||
		first.Items[0].ID == newest.ID ||
		first.Items[0].AccessID != accessID.String() ||
		first.Items[0].OccurredAt.IsZero() ||
		first.NextCursor == "" {
		t.Fatalf("first Activity page = %+v", first)
	}
	detailResponse := doRequest(
		t,
		router,
		authority,
		http.MethodGet,
		"/api/v1/exchanges/"+middle.SubjectID,
		readToken,
		nil,
	)
	if detailResponse.Code != http.StatusOK {
		t.Fatalf(
			"Exchange detail status=%d body=%s",
			detailResponse.Code,
			detailResponse.Body,
		)
	}
	detailPayload := append([]byte(nil), detailResponse.Body.Bytes()...)
	var detail desktopcontrol.ExchangeDetail
	decodeResponse(t, detailResponse, &detail)
	if detail.ID != middle.SubjectID ||
		detail.AccessID != accessID.String() ||
		detail.Status != string(activity.StatusFailed) ||
		detail.ProcessingTrace.Result != "private_failure_reason" ||
		detail.ProcessingTrace.PluginRunIDs == nil ||
		detail.ProcessingTrace.AttemptIDs == nil ||
		len(detail.ProcessingTrace.PluginRunIDs) != 0 ||
		len(detail.ProcessingTrace.AttemptIDs) != 0 {
		t.Fatalf("Exchange detail = %+v", detail)
	}
	for _, forbidden := range []string{
		`"sequence"`,
		`"occurredAt"`,
		`"diagnosis"`,
		`"transport"`,
		`"providerStatus"`,
		`"providerField"`,
	} {
		if strings.Contains(string(detailPayload), forbidden) {
			t.Fatalf("Exchange detail leaked %q: %s", forbidden, detailPayload)
		}
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(firstPayload, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope) != 2 || envelope["items"] == nil || envelope["nextCursor"] == nil {
		t.Fatalf("Activity page fields = %v", envelope)
	}
	var itemFields []map[string]json.RawMessage
	if err := json.Unmarshal(envelope["items"], &itemFields); err != nil {
		t.Fatal(err)
	}
	for _, item := range itemFields {
		if len(item) != 4 ||
			item["id"] == nil ||
			item["occurredAt"] == nil ||
			item["accessId"] == nil ||
			item["status"] == nil {
			t.Fatalf("Activity summary fields = %v", item)
		}
	}
	for _, forbidden := range []string{
		`"sequence"`,
		`"kind"`,
		`"subjectId"`,
		`"reasonCode"`,
		`"diagnosis"`,
		`"transport"`,
		"private_failure_reason",
	} {
		if strings.Contains(string(firstPayload), forbidden) {
			t.Fatalf("Activity response leaked %q: %s", forbidden, firstPayload)
		}
	}

	late := record(
		activity.KindExchangeCompleted,
		"exchange-route-late",
		activity.StatusSucceeded,
	)
	secondResponse := doRequest(
		t,
		router,
		authority,
		http.MethodGet,
		"/api/v1/activities?limit=2&cursor="+
			url.QueryEscape(first.NextCursor),
		readToken,
		nil,
	)
	if secondResponse.Code != http.StatusOK {
		t.Fatalf(
			"second Activity page status=%d body=%s",
			secondResponse.Code,
			secondResponse.Body,
		)
	}
	var second desktopcontrol.ActivityPage
	decodeResponse(t, secondResponse, &second)
	if len(second.Items) != 1 ||
		second.Items[0].ID != oldest.SubjectID ||
		second.Items[0].ID == late.SubjectID ||
		second.NextCursor != "" {
		t.Fatalf("second Activity page = %+v", second)
	}
	missing := doRequest(
		t,
		router,
		authority,
		http.MethodGet,
		"/api/v1/exchanges/exchange-route-missing",
		readToken,
		nil,
	)
	if missing.Code != http.StatusNotFound ||
		!strings.Contains(missing.Body.String(), "exchange_not_found") {
		t.Fatalf("missing Exchange response = %d %s", missing.Code, missing.Body)
	}
	invalidQuery := doRequest(
		t,
		router,
		authority,
		http.MethodGet,
		"/api/v1/exchanges/"+middle.SubjectID+"?include=body",
		readToken,
		nil,
	)
	if invalidQuery.Code != http.StatusUnprocessableEntity {
		t.Fatalf(
			"invalid Exchange query response = %d %s",
			invalidQuery.Code,
			invalidQuery.Body,
		)
	}
}

func TestActivityRouteRejectsNonCanonicalQueries(t *testing.T) {
	t.Parallel()

	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	router, authority, readToken := newActivityRouteFixture(t, runtime)

	for _, query := range []string{
		"cursor=",
		"cursor=not-a-cursor",
		"limit=",
		"limit=0",
		"limit=201",
		"limit=one",
		"unknown=1",
		"beforeSequence=1",
		"limit=1&limit=2",
		"cursor=one&cursor=two",
	} {
		response := doRequest(
			t,
			router,
			authority,
			http.MethodGet,
			"/api/v1/activities?"+query,
			readToken,
			nil,
		)
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf(
				"Activity query %q status=%d body=%s",
				query,
				response.Code,
				response.Body,
			)
		}
	}
}

func newActivityRouteFixture(
	t *testing.T,
	runtime *productruntime.Runtime,
) (http.Handler, string, string) {
	t.Helper()
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	readToken := capability(0x61)
	authenticator, err := desktopcontrol.NewAuthenticator(
		desktopcontrol.CapabilityGrant{
			ReadToken:  readToken,
			WriteToken: capability(0x62),
			ExpiresAt:  now.Add(time.Hour),
		},
		fixedClock{now: now},
	)
	if err != nil {
		t.Fatal(err)
	}
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness:     readyState(true),
		Status:        runtime,
		Accesses:      runtime.AccessWriter(),
		AccessCatalog: runtime.AccessCatalog(),
		Resolver:      runtime.SnapshotResolver(),
		Credentials:   runtime.Credentials(),
		Activities:    runtime.Activities(),
		Connections:   runtime.ConnectionEvents(),
		Egress:        runtime.EgressAttempts(),
		Approvals:     runtime.ToolApprovals(),
		Offline:       runtime,
	})
	if err != nil {
		t.Fatal(err)
	}
	const authority = "127.0.0.1:43129"
	router, err := desktopcontrol.NewRouter(desktopcontrol.RouterOptions{
		Authority:      authority,
		AllowedOrigins: []string{"tauri://localhost"},
		Authenticator:  authenticator,
		Application:    application,
		Bootstrap:      emptyBootstrap(),
		CaptureRuns:    http.NotFoundHandler(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return router, authority, readToken
}
