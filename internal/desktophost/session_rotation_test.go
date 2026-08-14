package desktophost_test

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
)

func TestHostWiresRecoverableControlSessionRotation(t *testing.T) {
	root := t.TempDir()
	paths := newHostPaths(t, filepath.Join(root, "cache"))
	host := startHost(t, hostOptions(t, paths, filepath.Join(root, "data")))
	defer shutdownHost(t, host)
	initial := exchangeBootstrap(t, host.Bootstrap())

	stateResponse := controlRequest(
		t,
		initial.BaseURL,
		http.MethodGet,
		desktopcontrol.SessionStatePath,
		initial.ReadToken,
		"vibermate://desktop",
	)
	defer stateResponse.Body.Close()
	if stateResponse.StatusCode != http.StatusOK {
		t.Fatalf("session state status=%d", stateResponse.StatusCode)
	}
	var state desktopcontrol.SessionState
	decodeStrictHostResponse(t, stateResponse, &state)
	if state.Schema != desktopcontrol.SessionStateSchema || state.Revision != 1 {
		t.Fatalf("initial session state = %+v", state)
	}

	const key = "host-session-renewal-0001"
	rotationResponse := hostSessionRenewalRequest(
		t,
		initial.BaseURL,
		initial.WriteToken,
		state.Revision,
		key,
	)
	defer rotationResponse.Body.Close()
	if rotationResponse.StatusCode != http.StatusOK {
		t.Fatalf("session rotation status=%d", rotationResponse.StatusCode)
	}
	var rotation desktopcontrol.SessionRotation
	decodeStrictHostResponse(t, rotationResponse, &rotation)
	if rotation.Schema != desktopcontrol.SessionRotationSchema ||
		rotation.Revision != 2 ||
		rotation.ReadToken == initial.ReadToken ||
		rotation.WriteToken == initial.WriteToken ||
		!rotation.ExpiresAt.After(state.ExpiresAt) {
		t.Fatalf("session rotation = %+v", rotation)
	}

	retiredRead := controlRequest(
		t,
		initial.BaseURL,
		http.MethodGet,
		"/api/v1/status",
		initial.ReadToken,
		"vibermate://desktop",
	)
	defer retiredRead.Body.Close()
	if retiredRead.StatusCode != http.StatusUnauthorized {
		t.Fatalf("retired read token status=%d", retiredRead.StatusCode)
	}
	retiredWrite := controlRequest(
		t,
		initial.BaseURL,
		http.MethodPost,
		"/api/v1/offline-hold/actions/enter",
		initial.WriteToken,
		"vibermate://desktop",
	)
	defer retiredWrite.Body.Close()
	if retiredWrite.StatusCode != http.StatusUnauthorized {
		t.Fatalf("retired write token status=%d", retiredWrite.StatusCode)
	}
	currentRead := controlRequest(
		t,
		initial.BaseURL,
		http.MethodGet,
		"/api/v1/status",
		rotation.ReadToken,
		"vibermate://desktop",
	)
	defer currentRead.Body.Close()
	if currentRead.StatusCode != http.StatusOK {
		t.Fatalf("rotated read token status=%d", currentRead.StatusCode)
	}

	// A retry after a lost response still carries the retired write token.
	// Only this exact key and revision can recover the committed generation.
	replayResponse := hostSessionRenewalRequest(
		t,
		initial.BaseURL,
		initial.WriteToken,
		state.Revision,
		key,
	)
	defer replayResponse.Body.Close()
	if replayResponse.StatusCode != http.StatusOK {
		t.Fatalf("session replay status=%d", replayResponse.StatusCode)
	}
	var replay desktopcontrol.SessionRotation
	decodeStrictHostResponse(t, replayResponse, &replay)
	if replay != rotation {
		t.Fatalf("replayed session = %+v, want %+v", replay, rotation)
	}
}

func hostSessionRenewalRequest(
	t *testing.T,
	baseURL string,
	token string,
	revision uint64,
	key string,
) *http.Response {
	t.Helper()
	request, err := http.NewRequest(
		http.MethodPost,
		baseURL+desktopcontrol.SessionRenewalPath,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", "vibermate://desktop")
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	request.Header.Set("Sec-Fetch-Mode", "cors")
	request.Header.Set("Sec-Fetch-Dest", "empty")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("If-Match", strconv.FormatUint(revision, 10))
	request.Header.Set("Idempotency-Key", key)
	transport := &http.Transport{Proxy: nil, DisableCompression: true}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func decodeStrictHostResponse(
	t *testing.T,
	response *http.Response,
	output any,
) {
	t.Helper()
	decoder := json.NewDecoder(response.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		t.Fatal(err)
	}
}
