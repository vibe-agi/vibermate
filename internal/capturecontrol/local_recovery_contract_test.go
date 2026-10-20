package capturecontrol_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
)

func TestLocalRecoveryLaunchMetadataBindsTheIssuedAuthority(t *testing.T) {
	fixture := newFixture(t)
	defer fixture.Close(t)
	response := fixture.DoJSON(t, http.MethodPost, "/api/v1/capture-runs?localRecovery=1", fixture.controlCredential, "", capturecontrol.CreateRequest{
		EnvironmentID: testEnvironmentID, CWD: fixture.workspace,
		Command: []string{"claude"}, ExecutablePath: fixture.executable,
	})
	if response.Code != http.StatusCreated {
		t.Fatalf("recovery launch: %d %s", response.Code, response.Body)
	}
	var wire struct {
		Run struct {
			ID string `json:"id"`
		} `json:"run"`
		LocalRecovery *struct {
			InitialEnvironmentID       string `json:"initialEnvironmentId"`
			InitialEnvironmentRevision uint64 `json:"initialEnvironmentRevision"`
			InitialEnvironmentDigest   string `json:"initialEnvironmentDigest"`
			LaunchAuthorityDigest      string `json:"launchAuthorityDigest"`
			CanNativeReview            bool   `json:"canNativeReview"`
		} `json:"localRecovery"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &wire); err != nil || wire.LocalRecovery == nil {
		t.Fatalf("negotiated launch omitted recovery metadata: %s, %v", response.Body, err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &object); err != nil {
		t.Fatal(err)
	}
	exactJSONObject(t, object["localRecovery"], []string{
		"initialEnvironmentId", "initialEnvironmentRevision", "initialEnvironmentDigest", "launchAuthorityDigest", "canNativeReview",
	})
	ref := captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: wire.Run.ID}
	authority, err := (fixedAuthorities{"api.anthropic.com:443"}).authoritySet(ref, testEnvironmentID, captureassignment.SourceLaunch)
	if err != nil {
		t.Fatal(err)
	}
	if got := wire.LocalRecovery; got.InitialEnvironmentID != authority.InitialEnvironmentID().String() ||
		got.InitialEnvironmentRevision != uint64(authority.InitialEnvironmentRevision()) ||
		got.InitialEnvironmentDigest != authority.InitialEnvironmentDigest().String() ||
		got.LaunchAuthorityDigest != authority.AuthorityDigest().String() || got.CanNativeReview {
		t.Fatalf("recovery metadata does not bind the actual launch authority: %+v", got)
	}
}

func TestLocalRecoveryMetadataIsAbsentFromLegacyLaunchWire(t *testing.T) {
	fixture := newFixture(t)
	defer fixture.Close(t)
	response := fixture.DoJSON(t, http.MethodPost, "/api/v1/capture-runs", fixture.controlCredential, "", capturecontrol.CreateRequest{
		EnvironmentID: testEnvironmentID, CWD: fixture.workspace,
		Command: []string{"claude"}, ExecutablePath: fixture.executable,
	})
	if response.Code != http.StatusCreated {
		t.Fatalf("legacy launch: %d %s", response.Code, response.Body)
	}
	exactJSONObject(t, response.Body.Bytes(), []string{
		"adapter", "catalogRevision", "executablePath", "launchEnvironment", "launchRecipe",
		"managedCredentialAuthorities", "protectedAuthorities", "proxyAddress", "proxyDelivery",
		"proxyToken", "recognition", "rootPemPath", "run", "runCapability",
	})
}

func TestLocalRecoveryLaunchValidationRejectsUnboundAuthorityMetadata(t *testing.T) {
	fixture := newFixture(t)
	defer fixture.Close(t)
	grant := fixture.createRun(t)
	if err := grant.Validate(); err != nil {
		t.Fatal(err)
	}
	ref := captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: grant.Run.ID}
	authority, err := (fixedAuthorities{"api.anthropic.com:443"}).authoritySet(ref, testEnvironmentID, captureassignment.SourceLaunch)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		field string
		value any
	}{
		{"initialEnvironmentId", "Bad ID"},
		{"initialEnvironmentRevision", 0},
		{"initialEnvironmentDigest", "invalid"},
		{"initialEnvironmentDigest", "0200000000000000000000000000000000000000000000000000000000000000"},
		{"launchAuthorityDigest", "0000000000000000000000000000000000000000000000000000000000000000"},
	} {
		t.Run(test.field+"/"+fmt.Sprint(test.value), func(t *testing.T) {
			encoded, err := json.Marshal(grant)
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]any
			if err = json.Unmarshal(encoded, &wire); err != nil {
				t.Fatal(err)
			}
			metadata := map[string]any{
				"initialEnvironmentId":       authority.InitialEnvironmentID().String(),
				"initialEnvironmentRevision": authority.InitialEnvironmentRevision(),
				"initialEnvironmentDigest":   authority.InitialEnvironmentDigest().String(),
				"launchAuthorityDigest":      authority.AuthorityDigest().String(),
				"canNativeReview":            false,
			}
			metadata[test.field] = test.value
			wire["localRecovery"] = metadata
			encoded, err = json.Marshal(wire)
			if err != nil {
				t.Fatal(err)
			}
			var received capturecontrol.LaunchGrant
			if err = json.Unmarshal(encoded, &received); err != nil {
				t.Fatal(err)
			}
			if received.Validate() == nil {
				t.Fatal("launcher accepted invalid or unbound recovery authority")
			}
		})
	}
}
