//go:build darwin

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/productruntime"
)

type packagedACPDiscoveryFunc func() (localdiscovery.Session, error)

func (f packagedACPDiscoveryFunc) Load() (localdiscovery.Session, error) { return f() }

func packagedACPStatusFixture(t *testing.T, states []productruntime.RecordingStatus, status int, wrongGeneration bool) (packagedACPDiscovery, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	instance := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("i", 16)))
	credential := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("c", 32)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := int(calls.Add(1)) - 1
		if r.Method != "GET" || r.URL.Path != "/api/v1/status" || r.Header.Get("Authorization") != "Bearer "+credential {
			t.Error("fixture used wrong method/path/CLI capability")
			w.WriteHeader(403)
			return
		}
		if status != 200 {
			w.WriteHeader(status)
			return
		}
		generation := instance
		if wrongGeneration {
			generation = "different-generation"
		}
		response := desktopcontrol.StatusResponse{Generation: generation, Ready: true, APIVersion: "v1", ProductBuild: "test-build", StatusKey: "runtime.state.initialized", Runtime: productruntime.RuntimeStatus{InstanceID: instance, State: productruntime.RuntimeStateInitialized, Host: "desktop", Storage: productruntime.StorageStateHealthy, StartedAt: time.Now().UTC(), Recording: states[min(index, len(states)-1)]}}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	expected := localdiscovery.Session{Schema: localdiscovery.Schema, InstanceID: instance, ProcessID: 1, BaseURL: server.URL, ControlCredential: credential, ExpiresAt: time.Now().Add(time.Minute)}
	source := packagedACPDiscoveryFunc(func() (localdiscovery.Session, error) {
		current := expected
		current.ExpiresAt = time.Now().Add(time.Minute)
		return current, nil
	})
	return packagedACPDiscovery{source: source, expected: expected}, &calls
}

func TestPackagedACPRecordingWaitsForOwnerAndSettlement(t *testing.T) {
	d, calls := packagedACPStatusFixture(t, []productruntime.RecordingStatus{{State: productruntime.RecordingStarting}, {State: productruntime.RecordingAvailable, ReservedRecords: 2, ReservedBytes: 64}, {State: productruntime.RecordingAvailable, CleanupPending: true}, {State: productruntime.RecordingAvailable}}, 200, false)
	if err := waitPackagedACPRecording(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 4 {
		t.Fatal("did not witness actual readiness and final settlement", calls.Load())
	}
}
func TestPackagedACPRecordingRefusesLossAndWrongAuthority(t *testing.T) {
	for _, test := range []struct {
		name            string
		state           productruntime.RecordingStatus
		status          int
		wrongGeneration bool
	}{
		{"dropped", productruntime.RecordingStatus{State: productruntime.RecordingAvailable, Dropped: 1}, 200, false},
		{"unconfirmed", productruntime.RecordingStatus{State: productruntime.RecordingAvailable, Unconfirmed: 1}, 200, false},
		{"disabled", productruntime.RecordingStatus{State: productruntime.RecordingDisabled}, 200, false},
		{"wrong_state", productruntime.RecordingStatus{State: "unknown"}, 200, false},
		{"unauthorized", productruntime.RecordingStatus{State: productruntime.RecordingAvailable}, 401, false},
		{"wrong_generation", productruntime.RecordingStatus{State: productruntime.RecordingAvailable}, 200, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			d, calls := packagedACPStatusFixture(t, []productruntime.RecordingStatus{test.state}, test.status, test.wrongGeneration)
			if err := waitPackagedACPRecording(t.Context(), d); err == nil {
				t.Fatal("invalid recording/authority accepted")
			}
			if calls.Load() != 1 {
				t.Fatal("wrong state/error retried", calls.Load())
			}
		})
	}
}
func TestPackagedACPRecordingWaitIsBounded(t *testing.T) {
	d, _ := packagedACPStatusFixture(t, []productruntime.RecordingStatus{{State: productruntime.RecordingStarting}}, 200, false)
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancel()
	if err := waitPackagedACPRecording(ctx, d); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("caller read budget not retained", err)
	}
}
func TestPackagedACPRecordingPinsCLIGeneration(t *testing.T) {
	d, calls := packagedACPStatusFixture(t, []productruntime.RecordingStatus{{State: productruntime.RecordingAvailable}}, 200, false)
	changed := d.expected
	changed.ProcessID++
	d.source = packagedACPDiscoveryFunc(func() (localdiscovery.Session, error) { return changed, nil })
	if err := waitPackagedACPRecording(t.Context(), d); err == nil || calls.Load() != 0 {
		t.Fatal("replacement discovery used", err, calls.Load())
	}
}
