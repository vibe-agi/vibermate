package productruntime

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
)

// The Runtime names a recognized run's client only from the catalog its host
// passed in (ADR 0026), on both proxy authorization and the run reader, and
// what it carries is the index its catalog and operator switch describe. A
// Runtime given no catalog names none, which fails closed.
func TestProductRuntimeIndexesRecognizedClientsFromItsCatalog(t *testing.T) {
	t.Parallel()

	catalog := clientadapter.BuiltInCatalog()
	for _, testCase := range []struct {
		name    string
		catalog clientadapter.Catalog
		state   clientadapter.ProtectionSwitch
		want    map[string]string
	}{
		{"with the host catalog", catalog, clientadapter.ProtectionsOn, map[string]string{
			"claude": "claude-code",
			"codex":  "codex-cli",
		}},
		{"with the host catalog and the switch off", catalog, clientadapter.ProtectionsOff, map[string]string{
			"claude": "claude-code",
			"codex":  "codex-cli",
		}},
		{"without a catalog", clientadapter.Catalog{}, clientadapter.ProtectionsOn, map[string]string{
			"claude": "",
			"codex":  "",
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			var index clientadapter.RecognizedClients
			if testCase.catalog.Valid() {
				var err error
				index, err = clientadapter.NewRecognizedClients(testCase.catalog, testCase.state)
				if err != nil {
					t.Fatal(err)
				}
			}
			options := testOptions(t, hostcontract.Desktop(), &coordinatorDouble{})
			options.ClientCatalog = testCase.catalog
			options.RecognizedClientProtections = testCase.state
			runtime := startTestRuntime(t, options)
			defer shutdownRuntime(t, runtime)
			for label, want := range testCase.want {
				evidence, view := recognizedRunEvidence(t, runtime, label, catalog.Revision())
				// The Runtime decides which client a run is and carries the
				// index's value for it, switch included; what the policy
				// grants that client is pinned in clientadapter.
				expected, _ := index.Client(catalog.Revision(), label)
				if expected.ID != want ||
					evidence.RecognizedClient != expected ||
					view.RecognizedClient != expected {
					t.Fatalf("%s: evidence %+v, view %+v, want %+v naming %q",
						label, evidence.RecognizedClient, view.RecognizedClient, expected, want)
				}
			}
			evidence, view := recognizedRunEvidence(t, runtime, "claude", catalog.Revision()-1)
			if evidence.RecognizedClient != (clientadapter.RecognizedClient{}) ||
				view.RecognizedClient != (clientadapter.RecognizedClient{}) {
				t.Fatalf("a run at another catalog revision named %+v", evidence.RecognizedClient)
			}
		})
	}
}

func TestProductRuntimeRefusesAnUnknownProtectionSwitch(t *testing.T) {
	t.Parallel()

	options := testOptions(t, hostcontract.Desktop(), &coordinatorDouble{})
	options.ClientCatalog = clientadapter.BuiltInCatalog()
	options.RecognizedClientProtections = clientadapter.ProtectionsOff + 1
	runtime, err := Start(context.Background(), options)
	if runtime != nil || !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("Start() = %v, %v", runtime, err)
	}
}

func recognizedRunEvidence(
	t *testing.T,
	runtime *Runtime,
	label string,
	revision clientadapter.CatalogRevision,
) (capturerun.Evidence, capturerun.View) {
	t.Helper()
	dir := t.TempDir()
	grant, err := runtime.CaptureRuns().Create(t.Context(), capturerun.CreateCommand{
		CWD:                     dir,
		CanonicalExecutablePath: filepath.Join(dir, label),
		ExecutableLabel:         label,
		CatalogRevision:         revision,
		Recognition:             clientadapter.RecognitionRecognized,
		Lifetime:                time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := runtime.CaptureRuns().Finish(context.Background(), grant.Run.ID, grant.ControlCapability); err != nil {
			t.Error(err)
		}
	}()
	evidence, err := runtime.captureRuns.AuthorizeProxy(t.Context(), grant.ProxyCapability)
	if err != nil {
		t.Fatal(err)
	}
	view, err := runtime.CaptureRunReader().GetRun(t.Context(), grant.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return evidence, view
}
