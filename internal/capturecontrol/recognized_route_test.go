package capturecontrol_test

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/capturegrant"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

// recognizingVerifier reports a client recognized by its publisher. A unit test
// cannot produce a code-signed file, and the signature check itself is covered
// where it belongs; what this exercises is what the grant route does with that
// answer.
type recognizingVerifier struct{}

func (recognizingVerifier) Verify(
	_ context.Context,
	request clientadapter.Request,
) (clientadapter.Detection, error) {
	// The real verifier resolves symlinks; a temporary directory on macOS is
	// reached through one, and the run store keeps canonical paths.
	canonical, err := filepath.EvalSymlinks(request.ExecutablePath)
	if err != nil {
		return clientadapter.Detection{}, err
	}
	return clientadapter.Detection{
		Status:          clientadapter.StatusGeneric,
		Recognition:     clientadapter.RecognitionRecognized,
		CatalogRevision: 4,
		CanonicalPath:   canonical,
		ExecutableLabel: "claude",
		Signer: &clientadapter.SignerEvidence{
			ID:              "claude-code",
			Revision:        1,
			CatalogRevision: 4,
			InstallShape:    clientadapter.InstallNativeSingleBinary,
			LaunchRecipe:    clientadapter.LaunchNodeEnvProxy,
			SignedPath:      canonical,
		},
	}, nil
}

type fixedApprover struct{ allow bool }

func (approver fixedApprover) AskClientRoot(
	_ context.Context,
	_ toolapproval.ClientRootAskRequest,
) (toolapproval.ClientRootAskOutcome, error) {
	return toolapproval.ClientRootAskOutcome{Allowed: approver.allow}, nil
}

type countingApprover struct{ calls int }

func (approver *countingApprover) AskClientRoot(
	_ context.Context,
	_ toolapproval.ClientRootAskRequest,
) (toolapproval.ClientRootAskOutcome, error) {
	approver.calls++
	return toolapproval.ClientRootAskOutcome{Allowed: true}, nil
}

// The grant is where the decision becomes an effect, so this is what has to
// distinguish an allow from a deny. Asserting only that the ask returns false
// would leave a route that ignores the answer passing.
func TestTheGrantCarriesTheRootOnlyWhenARecognizedClientWasAllowed(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name      string
		allow     bool
		wantRoot  bool
		wantRecip clientadapter.LaunchRecipe
	}{{
		name:      "allowed",
		allow:     true,
		wantRoot:  true,
		wantRecip: clientadapter.LaunchNodeEnvProxy,
	}, {
		name:      "denied",
		allow:     false,
		wantRoot:  false,
		wantRecip: clientadapter.LaunchGeneric,
	}} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			fixture := newFixture(t, func(options *capturegrant.Options) {
				options.Verifier = recognizingVerifier{}
				options.ClientRootApprovals = fixedApprover{allow: testCase.allow}
			})

			grant := fixture.createRun(t)
			if (grant.RootPEMPath != "") != testCase.wantRoot {
				t.Fatalf(
					"rootPemPath=%q with allow=%v",
					grant.RootPEMPath,
					testCase.allow,
				)
			}
			if grant.LaunchRecipe != testCase.wantRecip {
				t.Fatalf(
					"recipe=%q, want %q",
					grant.LaunchRecipe,
					testCase.wantRecip,
				)
			}
			if grant.Recognition != clientadapter.RecognitionRecognized {
				t.Fatalf("recognition=%q", grant.Recognition)
			}
			// The grant a launcher will actually receive has to pass the shape
			// both sides share. This is what nobody checked: the route and the
			// launcher each tested their own half, and the combination they
			// produced together could not start a client.
			if err := grant.Validate(); err != nil {
				t.Fatalf("the grant this route produced is not launchable: %v", err)
			}
			if testCase.allow {
				if grant.Signer == nil {
					t.Fatal("an allowed recognized grant carried no signer evidence")
				}
				if grant.Signer.LaunchRecipe != grant.LaunchRecipe ||
					grant.Signer.CatalogRevision != grant.CatalogRevision {
					t.Fatalf("signer evidence disagrees with its grant: %+v", grant.Signer)
				}
			} else if grant.Signer != nil {
				t.Fatalf("a denied grant carried signer evidence: %+v", grant.Signer)
			}
			// A recognized client is not a verified one, and the grant must
			// not describe it as though a build had been matched.
			if grant.Adapter != nil {
				t.Fatalf("a recognized grant carried release evidence: %+v", grant.Adapter)
			}
		})
	}
}

func TestRecognizedClientIsNotAskedForRootWithoutProtectedAuthorities(t *testing.T) {
	t.Parallel()

	approver := &countingApprover{}
	fixture := newFixture(t, func(options *capturegrant.Options) {
		options.Verifier = recognizingVerifier{}
		options.ClientRootApprovals = approver
		options.Authorities = fixedAuthorities{}
	})
	defer fixture.Close(t)

	grant := fixture.createRun(t)
	if approver.calls != 0 {
		t.Fatalf("Root approval calls = %d, want 0", approver.calls)
	}
	if grant.LaunchRecipe != clientadapter.LaunchGeneric ||
		grant.RootPEMPath != "" || grant.Adapter != nil || grant.Signer != nil ||
		len(grant.ProtectedAuthorities) != 0 {
		t.Fatalf("recognized transparent launch grant = %+v", grant)
	}
	if grant.Recognition != clientadapter.RecognitionRecognized {
		t.Fatalf("recognition = %q", grant.Recognition)
	}
	if err := grant.Validate(); err != nil {
		t.Fatalf("recognized transparent grant is invalid: %v", err)
	}
}

func (fixture *fixture) createRun(t *testing.T) capturecontrol.LaunchGrant {
	t.Helper()

	recorder := fixture.DoJSON(
		t,
		http.MethodPost,
		"/api/v1/capture-runs",
		fixture.controlCredential,
		"",
		capturecontrol.CreateRequest{
			CWD:            fixture.workspace,
			Command:        []string{"claude"},
			ExecutablePath: fixture.executable,
		},
	)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create returned %d: %s", recorder.Code, recorder.Body.String())
	}
	var grant capturecontrol.LaunchGrant
	if err := json.Unmarshal(recorder.Body.Bytes(), &grant); err != nil {
		t.Fatal(err)
	}
	return grant
}
