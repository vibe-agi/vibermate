package runtimepersistence

import (
	"context"
	"encoding/base64"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
	"github.com/vibe-agi/vibermate/internal/workspaceroute"
)

func TestWorkspaceRouteRepositoryCreatesCASUpdatesAndReopens(t *testing.T) {
	t.Parallel()
	databasePath := filepath.Join(t.TempDir(), "runtime.db")
	store := openTestStore(t, databasePath)
	repository := store.WorkspaceRouteRepository()
	accessID, err := access.NewAccessID("access-main")
	if err != nil {
		t.Fatal(err)
	}
	firstProfile, err := access.NewEndpointProfileID("profile-one")
	if err != nil {
		t.Fatal(err)
	}
	secondProfile, err := access.NewEndpointProfileID("profile-two")
	if err != nil {
		t.Fatal(err)
	}
	machineID := mustMachineID(t, 0x11)
	workspaceID := mustWorkspaceID(t, 0x22)
	bindingID, err := workspaceroute.BindingIDFor(accessID, machineID, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Unix(1_800_000_000, 0).UTC()
	request := workspaceroute.CreateRequest{
		ID:                          bindingID,
		AccessID:                    accessID,
		MachineID:                   machineID,
		WorkspaceID:                 workspaceID,
		MachineRegistrationRevision: 1,
		WorkspaceLabel:              "project",
		WorkspaceEvidence:           workspaceidentity.EvidenceLocalLauncher,
		ProfileID:                   firstProfile,
		UpdatedAt:                   createdAt,
	}
	created, err := repository.ResolveOrCreate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if created.Revision != 1 || created.ProfileID != firstProfile {
		t.Fatalf("created = %+v", created)
	}
	duplicate := request
	duplicate.ProfileID = secondProfile
	existing, err := repository.ResolveOrCreate(context.Background(), duplicate)
	if err != nil {
		t.Fatal(err)
	}
	if existing != created {
		t.Fatalf("ResolveOrCreate changed existing binding: got %+v want %+v", existing, created)
	}
	updatedAt := createdAt.Add(time.Minute)
	updated, err := repository.CompareAndSwap(
		context.Background(),
		bindingID,
		1,
		secondProfile,
		updatedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 || updated.ProfileID != secondProfile ||
		!updated.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("updated = %+v", updated)
	}
	if _, err := repository.CompareAndSwap(
		context.Background(),
		bindingID,
		1,
		firstProfile,
		updatedAt.Add(time.Minute),
	); !errors.Is(err, workspaceroute.ErrRevisionConflict) {
		t.Fatalf("stale CAS error = %v", err)
	}
	if err := store.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	reopened := openTestStore(t, databasePath)
	defer func() { _ = reopened.Shutdown(context.Background()) }()
	recovered, err := reopened.WorkspaceRouteRepository().Get(
		context.Background(),
		bindingID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if recovered != updated {
		t.Fatalf("recovered = %+v, want %+v", recovered, updated)
	}
	page, err := reopened.WorkspaceRouteRepository().List(
		context.Background(),
		workspaceroute.PageRequest{Limit: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0] != updated {
		t.Fatalf("page = %+v", page)
	}
}

func mustMachineID(t *testing.T, value byte) workspaceidentity.MachineID {
	t.Helper()
	id, err := workspaceidentity.ParseMachineID(
		base64.RawURLEncoding.EncodeToString(makeFilled(value)),
	)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustWorkspaceID(t *testing.T, value byte) workspaceidentity.WorkspaceID {
	t.Helper()
	id, err := workspaceidentity.ParseWorkspaceID(
		base64.RawURLEncoding.EncodeToString(makeFilled(value)),
	)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func makeFilled(value byte) []byte {
	data := make([]byte, 32)
	for index := range data {
		data[index] = value
	}
	return data
}
