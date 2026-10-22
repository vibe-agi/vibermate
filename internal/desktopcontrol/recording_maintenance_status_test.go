package desktopcontrol_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/productruntime"
)

type maintenanceDiagnosticStatus struct{ value productruntime.RuntimeStatus }

func (s maintenanceDiagnosticStatus) Status() productruntime.RuntimeStatus { return s.value }

func TestRecordingMaintenanceStatusRequiresExplicitOptIn(t *testing.T) {
	fixture := newAuditFixtureConfigured(t, nil, func(options *desktopcontrol.Options) {
		status := options.Status.Status()
		status.RecordingMaintenanceFailure = &productruntime.RecordingMaintenanceFailure{Phase: "compact", Reason: "busy", Recovery: "restart_required", At: time.Now().UTC()}
		status.RecordingFailure = &productruntime.PersistenceFailure{Operation: "usage", Reason: "write_failed", At: time.Now().UTC()}
		options.Status = maintenanceDiagnosticStatus{value: status}
	})
	for _, test := range []struct {
		query      string
		diagnostic bool
	}{{"", false}, {"?recordingDiagnostics=0", false}, {"?recordingDiagnostics=1&recordingDiagnostics=0", false}, {"?recordingDiagnostics=1", true}} {
		response := doRequest(t, fixture.router, fixture.authority, http.MethodGet, "/api/v1/status"+test.query, fixture.readToken, nil)
		var body struct {
			Runtime map[string]json.RawMessage `json:"runtime"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &body) != nil {
			t.Fatalf("status: %d %s", response.Code, response.Body)
		}
		_, exists := body.Runtime["recordingMaintenanceFailure"]
		if exists != test.diagnostic {
			t.Errorf("query %q diagnostic=%v, want %v; old clients reject unknown status fields", test.query, exists, test.diagnostic)
		}
		if _, exists := body.Runtime["recordingFailure"]; !exists {
			t.Fatal("maintenance diagnostic replaced earlier recording gap")
		}
	}
}
