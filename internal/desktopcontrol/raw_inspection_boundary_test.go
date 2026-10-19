package desktopcontrol_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

type rawHTTPBoundaryReader struct {
	*runtimepersistence.RawInspectionReader
	afterRead func()
	checks    int
}

func (r *rawHTTPBoundaryReader) Reveal(ctx context.Context, q rawevidence.RevealRequest) (rawevidence.RevealedEnvelope, error) {
	value, err := r.RawInspectionReader.Reveal(ctx, q)
	if err == nil {
		r.afterRead()
	}
	return value, err
}
func (r *rawHTTPBoundaryReader) ListExchange(ctx context.Context, id string) ([]rawevidence.EnvelopeMetadata, error) {
	values, err := r.RawInspectionReader.ListExchange(ctx, id)
	if err == nil {
		r.afterRead()
	}
	return values, err
}
func (r *rawHTTPBoundaryReader) Check(ctx context.Context, token recording.InspectionToken) error {
	r.checks++
	return r.RawInspectionReader.Check(ctx, token)
}

type rawHTTPHeaderProof struct{}

func (rawHTTPHeaderProof) ReadOverwriteHeader(_ context.Context, q providerauth.HeaderLookup) (string, error) {
	if q.AccountID != "synthetic-account" || q.AccountRevision != 2 || q.CredentialEpoch != 3 || q.UpstreamEndpointID != "synthetic-endpoint" || q.UpstreamEndpointRevision != 4 {
		return "", errors.New("unfrozen header lookup")
	}
	return "synthetic-header-proof", nil
}
func (r *rawHTTPBoundaryReader) RevealHeader(ctx context.Context, q rawevidence.HeaderRevealRequest, _ providerauth.HeaderReader) (rawevidence.RevealedHeader, error) {
	value, err := r.RawInspectionReader.RevealHeader(ctx, q, rawHTTPHeaderProof{})
	if err == nil {
		r.afterRead()
	}
	return value, err
}

func TestRawInspectionHTTPRechecksAfterIndependentReadAndEnrichment(t *testing.T) {
	for _, view := range []string{"list", "decoded", "image", "header"} {
		t.Run(view, func(t *testing.T) {
			ctx := context.Background()
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			controlPath := filepath.Join(root, "control.db")
			observationPath := filepath.Join(root, "observations.db")
			control, err := runtimepersistence.OpenControl(ctx, runtimepersistence.ControlOptions{DatabasePath: controlPath, LayoutID: "raw-http", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer control.Shutdown(ctx)
			observations, err := runtimepersistence.OpenObservations(ctx, runtimepersistence.ObservationOptions{DatabasePath: observationPath, LayoutID: "raw-http", ArchiveID: "current", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer observations.Shutdown(ctx)
			budget, _ := recording.NewBudget(recording.Limits{Records: 2048, Bytes: 64 << 20})
			manager, err := rawevidence.Open(ctx, rawevidence.Options{Repository: observations.RawEvidenceRepository(), Budget: budget, Random: rand.Reader, Clock: rawevidence.SystemClock{}, Config: rawevidence.DefaultConfig()})
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Shutdown(ctx)
			var pixels bytes.Buffer
			if err = png.Encode(&pixels, image.NewNRGBA(image.Rect(0, 0, 2, 1))); err != nil {
				t.Fatal(err)
			}
			part, _ := json.Marshal(map[string]string{"type": "input_image", "image_url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(pixels.Bytes())})
			fingerprint := sha256.Sum256(part)
			body := []byte(`{"input":[{"role":"user","content":[` + string(part) + `]}],"private":"withheld-after-read"}`)
			epoch, _ := recording.NewEpoch(1)
			stamp, err := recording.NewStamp(epoch, captureidentity.Reference{}, recording.Fence{ArchiveEpoch: 1})
			if err != nil {
				t.Fatal(err)
			}
			observation := rawevidence.Observation{Context: rawevidence.Context{ScopeKind: rawevidence.ScopeRuntime, ExchangeID: "raw-http-exchange", Recording: rawevidence.RecordingFull, RetentionDays: 1}, Layer: rawevidence.LayerClientIngress, Complete: true, Representation: "http_message", Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: body}
			if view == "header" {
				observation.Layer = rawevidence.LayerProviderEgress
				observation.AccountID = "synthetic-account"
				observation.AccountRevision = 2
				observation.CredentialEpoch = 3
				observation.UpstreamEndpointID = "synthetic-endpoint"
				observation.UpstreamEndpointRevision = 4
				observation.Headers.Set("User-Agent", "synthetic-header-proof")
				observation.ProtectedHeaderNames = []string{"User-Agent"}
			}
			if result := manager.TryObserve(stamp, observation); result != recording.Accepted {
				t.Fatalf("current original scope was not admitted: %v", result)
			}
			if err = manager.Flush(ctx, rawevidence.Watermark{}); err != nil {
				t.Fatal(err)
			}

			records, err := observations.RawEvidenceRepository().ListExchange(ctx, "raw-http-exchange")
			if err != nil || len(records) != 1 {
				t.Fatalf("actual recorded root: %v", err)
			}
			db, err := sql.Open("sqlite", observationPath)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			policy, err := control.UsagePolicyRepository().UsagePolicy(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var since any
			if policy.CollectingSince != nil {
				since = policy.CollectingSince.UnixMilli()
			}
			if _, err = db.Exec(`INSERT INTO inspection_mirror_generations VALUES(1,'raw-http',1,1,1,?,?,?,0,0,0,1)`, policy.Enabled, policy.RetentionDays, since); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`INSERT INTO inspection_mirror_active VALUES(1,1)`); err != nil {
				t.Fatal(err)
			}
			catalog, err := runtimepersistence.NewInspectionCatalog(runtimepersistence.InspectionCatalogOptions{Root: root, LayoutID: "raw-http", Generation: 1, Sources: []runtimepersistence.InspectionSource{{ID: "current", RelativePath: "observations.db", Database: runtimepersistence.SchemaState{Identity: "vibermate-observations", Revision: 1}, Current: true}}})
			if err != nil {
				t.Fatal(err)
			}
			defer catalog.Close(ctx)
			controlDB, err := sql.Open("sqlite", controlPath)
			if err != nil {
				t.Fatal(err)
			}
			defer controlDB.Close()
			reader := &rawHTTPBoundaryReader{RawInspectionReader: runtimepersistence.NewRawInspectionReader(catalog, control, observations, manager), afterRead: func() {
				if _, err := controlDB.Exec(`UPDATE runtime_storage_state SET visibility_revision=visibility_revision+1`); err != nil {
					t.Fatal(err)
				}
			}}
			fixture := newAuditFixtureWithRawEvidence(t, reader)
			path := "/api/v1/exchanges/raw-http-exchange/raw-evidence"
			method := http.MethodGet
			if view != "list" {
				method = http.MethodPost
				path = "/api/v1/raw-evidence/" + records[0].EnvelopeID + "/actions/reveal?bodyView=" + view
				if view == "image" {
					path += fmt.Sprintf("&fingerprint=sha256:%x", fingerprint)
				}
			}
			var requestBody []byte
			if view == "header" {
				path = "/api/v1/raw-evidence/" + records[0].EnvelopeID + "/actions/reveal-header"
				requestBody = []byte(`{"headerName":"User-Agent"}`)
			}
			token := fixture.writeToken
			if view == "list" {
				token = fixture.readToken
			}
			response := doRequest(t, fixture.router, fixture.authority, method, path, token, requestBody)
			if response.Code == 200 || reader.checks != 1 || bytes.Contains(response.Body.Bytes(), []byte("withheld-after-read")) {
				t.Fatalf("stale outer result published: status=%d checks=%d", response.Code, reader.checks)
			}
			if view != "list" {
				var count int
				if err = db.QueryRow(`SELECT count(*) FROM runtime_raw_inspection_reveal_audits`).Scan(&count); err != nil || count != 1 {
					t.Fatal("read bypassed actual audit", err)
				}
			}
		})
	}
}
