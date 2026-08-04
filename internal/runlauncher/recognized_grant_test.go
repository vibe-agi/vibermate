package runlauncher

import (
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
)

func recognizedRun() capturecontrol.CaptureRunView {
	createdAt := time.Date(2026, 8, 3, 8, 0, 0, 0, time.UTC)
	return capturecontrol.CaptureRunView{
		ID:                 "run-recognized",
		ExecutableLabel:    "claude",
		CWD:                "/opt/example",
		CreatedAt:          createdAt,
		ExpiresAt:          createdAt.Add(time.Minute),
		ClientAdapterState: clientadapter.StatusGeneric,
		ClientRecognition:  clientadapter.RecognitionRecognized,
		CatalogRevision:    4,
	}
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
		Signer: &capturecontrol.ClientSignerView{
			ID:              "claude-code",
			Revision:        1,
			CatalogRevision: 4,
			InstallShape:    clientadapter.InstallNativeSingleBinary,
			LaunchRecipe:    clientadapter.LaunchNodeEnvProxy,
			SignedPath:      "/opt/example/claude",
		},
		ExecutablePath:               "/opt/example/claude",
		ProxyAddress:                 "http://127.0.0.1:32123",
		ProxyToken:                   "proxy-capability",
		RunCapability:                "run-capability",
		RootPEMPath:                  "/tmp/root.pem",
		ProtectedAuthorities:         []string{},
		ManagedCredentialAuthorities: []string{},
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
		Run:                          recognizedRun(),
		CatalogRevision:              4,
		LaunchRecipe:                 clientadapter.LaunchNodeEnvProxy,
		Recognition:                  clientadapter.RecognitionRecognized,
		ExecutablePath:               "/opt/example/claude",
		ProxyAddress:                 "http://127.0.0.1:32123",
		ProxyToken:                   "proxy-capability",
		RunCapability:                "run-capability",
		RootPEMPath:                  "/tmp/root.pem",
		ProtectedAuthorities:         []string{},
		ManagedCredentialAuthorities: []string{},
	}
	if err := validateGrant(grant); err == nil {
		t.Fatal("a Root-bearing grant with no evidence at all was accepted")
	}
}

func TestManagedCredentialAuthoritiesAreAUniqueProtectedSubset(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name    string
		managed []string
	}{
		{
			name:    "outside protected set",
			managed: []string{"other.example:443"},
		},
		{
			name: "duplicate",
			managed: []string{
				"api.anthropic.com:443",
				"api.anthropic.com:443",
			},
		},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			grant := capturecontrol.LaunchGrant{
				Run:             recognizedRun(),
				CatalogRevision: 4,
				LaunchRecipe:    clientadapter.LaunchNodeEnvProxy,
				Recognition:     clientadapter.RecognitionRecognized,
				Signer: &capturecontrol.ClientSignerView{
					ID:              "claude-code",
					Revision:        1,
					CatalogRevision: 4,
					InstallShape:    clientadapter.InstallNativeSingleBinary,
					LaunchRecipe:    clientadapter.LaunchNodeEnvProxy,
					SignedPath:      "/opt/example/claude",
				},
				ExecutablePath: "/opt/example/claude",
				ProxyAddress:   "http://127.0.0.1:32123",
				ProxyToken:     "proxy-capability",
				RunCapability:  "run-capability",
				RootPEMPath:    "/tmp/root.pem",
				ProtectedAuthorities: []string{
					"api.anthropic.com:443",
				},
				ManagedCredentialAuthorities: append(
					[]string(nil),
					testCase.managed...,
				),
			}
			if err := validateGrant(grant); err == nil {
				t.Fatalf("%s managed authorities were accepted", testCase.name)
			}
		})
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
			grant.Adapter = &capturecontrol.ClientAdapterView{
				ID:              "claude-code",
				Revision:        1,
				Version:         "2.1.220",
				CatalogRevision: 4,
				Source: capturecontrol.
					ClientAdapterSourcePrelaunchDigestCatalog,
				InstallShape: clientadapter.InstallNativeSingleBinary,
				LaunchRecipe: clientadapter.LaunchNodeEnvProxy,
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
				Signer: &capturecontrol.ClientSignerView{
					ID:              "claude-code",
					Revision:        1,
					CatalogRevision: 4,
					InstallShape:    clientadapter.InstallNativeSingleBinary,
					LaunchRecipe:    clientadapter.LaunchNodeEnvProxy,
					SignedPath:      "/opt/example/claude",
				},
				ExecutablePath:               "/opt/example/claude",
				ProxyAddress:                 "http://127.0.0.1:32123",
				ProxyToken:                   "proxy-capability",
				RunCapability:                "run-capability",
				RootPEMPath:                  "/tmp/root.pem",
				ProtectedAuthorities:         []string{},
				ManagedCredentialAuthorities: []string{},
			}
			testCase.mutate(&grant)
			if err := validateGrant(grant); err == nil {
				t.Fatalf("%s was accepted", testCase.name)
			}
		})
	}
}
