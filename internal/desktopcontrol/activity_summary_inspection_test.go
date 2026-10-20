package desktopcontrol_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

func TestActivitySummaryHTTPClassifiesMissingMirrorAfterBoundary(t *testing.T) {
	fixture, reader, ref, _ := captureObservationHTTPFixture(t, func(o *desktopcontrol.Options, r activityHTTPRoles) {
		r.seed(t, "private-summary-request")
		db, err := sql.Open("sqlite", filepath.Join(r.root, "observations.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err = db.Exec(`DELETE FROM inspection_mirror_active`); err != nil {
			t.Fatal(err)
		}
		// Begin checks control visibility and catalog state. The actual read
		// must still reject the missing optional observation mirror.
		if _, err = o.ActivityInspection.Begin(t.Context()); err != nil {
			t.Fatalf("initial inspection boundary: %v", err)
		}
	})
	path := "/api/v1/activities/summary?manualCaptureId=" + ref.ID
	unauthorized := doRequest(t, fixture.router, fixture.authority, http.MethodGet, path, fixture.writeToken, nil)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("inspection failure replaced the read authority guard: %d %s", unauthorized.Code, unauthorized.Body)
	}
	response := doRequest(t, fixture.router, fixture.authority, http.MethodGet, path, fixture.readToken, nil)
	assertSummaryInspectionProblem(t, response, http.StatusServiceUnavailable, "inspection_unavailable")
	if reader.calls != 0 {
		t.Fatalf("Capture observation read after failed request summary: %d", reader.calls)
	}
}

type summaryHTTPVisibility struct {
	recording.InspectionVisibility
	beforeCheck func()
}

func (v *summaryHTTPVisibility) RecheckInspectionStamp(ctx context.Context, stamp recording.InspectionStamp) error {
	v.beforeCheck()
	return v.InspectionVisibility.RecheckInspectionStamp(ctx, stamp)
}

func TestActivitySummaryHTTPClassifiesVisibilityChangeDuringRead(t *testing.T) {
	checked := false
	fixture, reader, ref, _ := captureObservationHTTPFixture(t, func(o *desktopcontrol.Options, r activityHTTPRoles) {
		r.seed(t, "private-summary-request")
		limits, err := exchangecontent.DefaultSourceLimits()
		if err != nil {
			t.Fatal(err)
		}
		visibility := &summaryHTTPVisibility{InspectionVisibility: r.control, beforeCheck: func() {
			checked = true
			r.changeVisibility(t)
		}}
		o.ActivityInspection, err = runtimepersistence.NewActivityInspectionReader(r.catalog, visibility, limits)
		if err != nil {
			t.Fatal(err)
		}
	})
	response := doRequest(t, fixture.router, fixture.authority, http.MethodGet, "/api/v1/activities/summary?manualCaptureId="+ref.ID, fixture.readToken, nil)
	assertSummaryInspectionProblem(t, response, http.StatusConflict, "inspection_changed")
	if !checked || reader.calls != 0 {
		t.Fatalf("summary did not fail at its actual visibility recheck: checked=%v Capture reads=%d", checked, reader.calls)
	}
}

func assertSummaryInspectionProblem(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var value map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatalf("invalid summary problem: %d %s", response.Code, response.Body)
	}
	var actualCode string
	if err := json.Unmarshal(value["code"], &actualCode); err != nil || response.Code != status || actualCode != code {
		t.Fatalf("summary problem: want %d %s, got %d %s", status, code, response.Code, response.Body)
	}
	for _, field := range []string{"scope", "requests", "captureObservation", "contentBodiesAvailable", "failures", "firstObservedAt", "lastObservedAt"} {
		if _, exists := value[field]; exists {
			t.Fatalf("failed inspection exposed summary field %q: %s", field, response.Body)
		}
	}
	if strings.Contains(response.Body.String(), "private-summary-request") {
		t.Fatalf("failed inspection exposed request evidence: %s", response.Body)
	}
}
