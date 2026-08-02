package runlauncher

import (
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
)

func recognizedRun() capturerun.View {
	return capturerun.View{ID: "run-recognized"}
}

// The grant a recognized client actually produces has to be one the launcher
// accepts.
//
// It was not. `capturecontrol` answered an approved recognized launch with a
// Root-bearing recipe and no adapter evidence, because a recognized client has
// no release evidence by definition; the launcher rejected exactly that
// combination as "omitted verified adapter evidence". Both packages' own tests
// passed. Nothing exercised the contract between them, so the entire tier was
// dead on arrival and the approval prompt led to a client that could not start.
func TestARecognizedGrantIsAcceptedByTheLauncher(t *testing.T) {
	t.Parallel()

	grant := capturecontrol.LaunchGrant{
		Run:             recognizedRun(),
		CatalogRevision: 4,
		LaunchRecipe:    clientadapter.LaunchNodeEnvProxy,
		Recognition:     clientadapter.RecognitionRecognized,
		Signer: &clientadapter.SignerEvidence{
			ID:              "claude-code",
			Revision:        1,
			CatalogRevision: 4,
			InstallShape:    clientadapter.InstallNativeSingleBinary,
			LaunchRecipe:    clientadapter.LaunchNodeEnvProxy,
			SignedPath:      "/opt/example/claude",
		},
		ExecutablePath:  "/opt/example/claude",
		ProxyOrigin:     "http://127.0.0.1:32123",
		ProxyCapability: "proxy-capability",
		RunCapability:   "run-capability",
		RootPEMPath:     "/tmp/root.pem",
	}
	if err := validateGrant(grant); err != nil {
		t.Fatalf("a recognized grant was refused: %v", err)
	}
}

// The tier may not become a way around release evidence. A recognized grant
// carries signer evidence or it carries nothing.
func TestARootBearingGrantWithNeitherEvidenceIsRefused(t *testing.T) {
	t.Parallel()

	grant := capturecontrol.LaunchGrant{
		Run:             recognizedRun(),
		CatalogRevision: 4,
		LaunchRecipe:    clientadapter.LaunchNodeEnvProxy,
		Recognition:     clientadapter.RecognitionRecognized,
		ExecutablePath:  "/opt/example/claude",
		ProxyOrigin:     "http://127.0.0.1:32123",
		ProxyCapability: "proxy-capability",
		RunCapability:   "run-capability",
		RootPEMPath:     "/tmp/root.pem",
	}
	if err := validateGrant(grant); err == nil {
		t.Fatal("a Root-bearing grant with no evidence at all was accepted")
	}
}

// Signer evidence must agree with the grant it arrived in, the same way
// release evidence must.
func TestSignerEvidenceMustAgreeWithItsGrant(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name   string
		mutate func(*capturecontrol.LaunchGrant)
	}{{
		name: "a different catalogue revision",
		mutate: func(grant *capturecontrol.LaunchGrant) {
			grant.Signer.CatalogRevision = 9
		},
	}, {
		name: "a different recipe",
		mutate: func(grant *capturecontrol.LaunchGrant) {
			grant.Signer.LaunchRecipe = clientadapter.LaunchSSLCertFile
		},
	}, {
		name: "a recognition tier that does not match the evidence",
		mutate: func(grant *capturecontrol.LaunchGrant) {
			grant.Recognition = clientadapter.RecognitionUnverified
		},
	}, {
		name: "signer evidence alongside release evidence",
		mutate: func(grant *capturecontrol.LaunchGrant) {
			grant.Adapter = &clientadapter.Evidence{
				ID:              "claude-code",
				Revision:        1,
				Version:         "2.1.220",
				CatalogRevision: 4,
				InstallShape:    clientadapter.InstallNativeSingleBinary,
				ReleaseSHA256:   strings.Repeat("a", 64),
				LaunchRecipe:    clientadapter.LaunchNodeEnvProxy,
			}
		},
	}} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			grant := capturecontrol.LaunchGrant{
				Run:             recognizedRun(),
				CatalogRevision: 4,
				LaunchRecipe:    clientadapter.LaunchNodeEnvProxy,
				Recognition:     clientadapter.RecognitionRecognized,
				Signer: &clientadapter.SignerEvidence{
					ID:              "claude-code",
					Revision:        1,
					CatalogRevision: 4,
					InstallShape:    clientadapter.InstallNativeSingleBinary,
					LaunchRecipe:    clientadapter.LaunchNodeEnvProxy,
					SignedPath:      "/opt/example/claude",
				},
				ExecutablePath:  "/opt/example/claude",
				ProxyOrigin:     "http://127.0.0.1:32123",
				ProxyCapability: "proxy-capability",
				RunCapability:   "run-capability",
				RootPEMPath:     "/tmp/root.pem",
			}
			testCase.mutate(&grant)
			if err := validateGrant(grant); err == nil {
				t.Fatalf("%s was accepted", testCase.name)
			}
		})
	}
}
