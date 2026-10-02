package capturecontrol

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/controlprincipal"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

type environmentListFixture struct {
	environment.Reader
	items []environment.EnvironmentSnapshot
	calls int
}

func (reader *environmentListFixture) List(context.Context) ([]environment.EnvironmentSnapshot, error) {
	reader.calls++
	return reader.items, nil
}

func TestCaptureEnvironmentCatalogIsScopedAndBodyFree(t *testing.T) {
	compiler, err := environment.NewCompiler(nil, nil, protocolspec.Catalog{}, wireprofile.Catalog{})
	if err != nil {
		t.Fatal(err)
	}
	reader := &environmentListFixture{}
	for _, item := range []struct {
		id    string
		state environment.State
	}{{"work", environment.StateActive}, {"personal", environment.StateActive}, {"disabled", environment.StateDisabled}} {
		snapshot, err := compiler.Restore(environment.Environment{
			ID: environment.EnvironmentID(item.id), Name: item.id, State: item.state, Revision: 1,
			ContentRecording:  environment.DefaultContentRecordingPolicy(),
			LaunchEnvironment: environment.LaunchEnvironmentPolicy{SetEnv: map[string]string{"PRIVATE_SETTING": "must-not-be-returned"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		reader.items = append(reader.items, snapshot)
	}
	token := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	policy, err := runtimeuser.NewPolicy(false, []string{"work"}, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		attributes controlprincipal.Attributes
		want       int
	}{
		{"local", controlprincipal.Attributes{Kind: controlprincipal.KindLocalCLI}, 2},
		{"member", controlprincipal.Attributes{Kind: controlprincipal.KindRuntimeUser, MachineID: "machine.one", DeviceName: "Paseo", RuntimeUserID: "user.one", RuntimeUsername: "member", LoginSessionID: "session.one", RuntimeUserPolicy: policy}, 1},
		{"no grant", controlprincipal.Attributes{Kind: controlprincipal.KindLocalCLI, AllowedGrantKinds: []controlprincipal.GrantKind{controlprincipal.GrantManualCapture}}, -1},
	} {
		t.Run(test.name, func(t *testing.T) {
			attributes := test.attributes
			attributes.ID, attributes.CredentialRevision = "principal.one", 1
			if attributes.AllowedGrantKinds == nil {
				attributes.AllowedGrantKinds = []controlprincipal.GrantKind{controlprincipal.GrantCaptureRun}
			}
			principal, err := controlprincipal.New(attributes)
			if err != nil {
				t.Fatal(err)
			}
			auth, err := controlprincipal.NewAuthority(controlprincipal.CredentialGrant{Credential: token, Principal: principal})
			if err != nil {
				t.Fatal(err)
			}
			handler := &Handler{principals: auth, environments: reader}
			for _, bad := range []string{"credential", "capability", "proxy credential", "query", "empty query", "body"} {
				request := httptest.NewRequest(http.MethodGet, EnvironmentsPath, nil)
				request.Header.Set("Authorization", "Bearer "+token)
				switch bad {
				case "credential":
					request.Header.Del("Authorization")
				case "capability":
					request.Header.Set(RunCapabilityHeader, "not-a-catalog-credential")
				case "proxy credential":
					request.Header.Set("Proxy-Authorization", "Basic not-a-catalog-credential")
				case "query":
					request.URL.RawQuery = "includeSecrets=true"
				case "empty query":
					request.URL.ForceQuery = true
				case "body":
					request = httptest.NewRequest(http.MethodGet, EnvironmentsPath, strings.NewReader("{}"))
					request.Header.Set("Authorization", "Bearer "+token)
				}
				before := reader.calls
				response := httptest.NewRecorder()
				handler.listEnvironments(response, request)
				if response.Code < 400 || reader.calls != before {
					t.Fatalf("%s reached catalog: status=%d", bad, response.Code)
				}
			}
			request := httptest.NewRequest(http.MethodGet, EnvironmentsPath, nil)
			request.Header.Set("Authorization", "Bearer "+token)
			response := httptest.NewRecorder()
			handler.listEnvironments(response, request)
			if test.want < 0 {
				if response.Code != http.StatusForbidden {
					t.Fatalf("no grant: status=%d", response.Code)
				}
				return
			}
			var page CaptureEnvironments
			if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusOK || page.Schema != EnvironmentsSchema || len(page.Items) != test.want ||
				strings.Contains(response.Body.String(), "must-not-be-returned") || strings.Contains(response.Body.String(), "disabled") ||
				test.name == "member" && page.Items[0].ID != "work" {
				t.Fatalf("catalog status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}
