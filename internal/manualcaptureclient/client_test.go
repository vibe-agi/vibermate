package manualcaptureclient

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/capturecredential"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
)

func TestClientUsesConfirmationTokenAndOpaqueETag(t *testing.T) {
	t.Parallel()

	controlCredential := encodedBytes(0x21, 32)
	confirmationToken := "ctx_" + encodedBytes(0x31, 32)
	stateTag := `"mc_` + encodedBytes(0x41, 32) + `"`
	proxyCredential, err := capturecredential.New(
		capturecredential.KindManualCapture,
		bytes.Repeat([]byte{0x51}, capturecredential.EntropyBytes),
	)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 4, 8, 0, 0, 0, time.UTC)
	view := capturecontrol.ManualCaptureView{
		ID:               "manual-one",
		IngressProfileID: "manual-capture/manual-one",
		DisplayName:      "Project terminal",
		ClientClass:      manualcapture.ClientCLI,
		Lifetime:         manualcapture.LifetimeTemporary,
		State:            manualcapture.StateActive,
		Observation:      manualcapture.ObservationWaiting,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		calls.Add(1)
		if request.Header.Get("Authorization") != "Bearer "+controlCredential {
			t.Errorf("Authorization=%q", request.Header.Get("Authorization"))
		}
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /api/v1/manual-captures/context":
			_ = json.NewEncoder(writer).Encode(capturecontrol.ManualCaptureContext{
				ConfirmationToken: confirmationToken,
				ProxyAddress:      "http://127.0.0.1:32123",
				Root: capturecontrol.RootPublicDelivery{
					Kind:        "local_path",
					DERSHA256:   strings.Repeat("a", 64),
					Fingerprint: "AA:AA",
					PEMPath:     "/private/root.pem",
				},
				DefaultTemporarySeconds: 3600,
				MaxTemporarySeconds:     7200,
			})
		case "POST /api/v1/manual-captures":
			var input capturecontrol.ManualCaptureCreateRequest
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			if input.ConfirmationToken != confirmationToken {
				t.Errorf("confirmation token=%q", input.ConfirmationToken)
			}
			writer.Header().Set("ETag", stateTag)
			writer.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(writer).Encode(capturecontrol.ManualCaptureGrant{
				Capture:       view,
				ProxyAddress:  "http://127.0.0.1:32123",
				ProxyUsername: manualcapture.ProxyUsername,
				ProxyPassword: proxyCredential.Value(),
				Root: capturecontrol.RootPublicDelivery{
					Kind:        "local_path",
					DERSHA256:   strings.Repeat("a", 64),
					Fingerprint: "AA:AA",
					PEMPath:     "/private/root.pem",
				},
			})
		case "GET /api/v1/manual-captures/manual-one":
			writer.Header().Set("ETag", stateTag)
			_ = json.NewEncoder(writer).Encode(view)
		case "POST /api/v1/manual-captures/manual-one/actions/revoke":
			if request.Header.Get("If-Match") != stateTag {
				t.Errorf("If-Match=%q", request.Header.Get("If-Match"))
			}
			writer.Header().Del("Content-Type")
			writer.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client, err := New(testSession(server.URL, controlCredential))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx := context.Background()
	captureContext, err := client.Context(ctx)
	if err != nil || captureContext.ConfirmationToken != confirmationToken {
		t.Fatalf("Context()=%+v err=%v", captureContext, err)
	}
	expires := int64(3600)
	created, err := client.Create(ctx, capturecontrol.ManualCaptureCreateRequest{
		DisplayName:       view.DisplayName,
		ClientClass:       view.ClientClass,
		Lifetime:          view.Lifetime,
		ExpiresInSeconds:  &expires,
		ConfirmationToken: captureContext.ConfirmationToken,
	})
	if err != nil || created.ETag != stateTag || created.Grant.Capture.ID != view.ID {
		t.Fatalf("Create()=%+v err=%v", created, err)
	}
	id, err := manualcapture.ParseID(view.ID)
	if err != nil {
		t.Fatal(err)
	}
	resource, err := client.Get(ctx, id)
	if err != nil || resource.ETag != stateTag || resource.Capture.ID != view.ID {
		t.Fatalf("Get()=%+v err=%v", resource, err)
	}
	if err := client.Revoke(ctx, id, resource.ETag); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 4 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestClientRejectsNumericOrMissingStateTags(t *testing.T) {
	t.Parallel()

	id, err := manualcapture.ParseID("manual-one")
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{}
	for _, value := range []string{"", "1", `"revision-1"`, "mc_" + encodedBytes(1, 32)} {
		if _, err := client.Rotate(context.Background(), id, value); err == nil {
			t.Fatalf("Rotate accepted state tag %q", value)
		}
		if err := client.Revoke(context.Background(), id, value); err == nil {
			t.Fatalf("Revoke accepted state tag %q", value)
		}
	}
}

func testSession(origin, credential string) localdiscovery.Session {
	return localdiscovery.Session{
		Schema:            localdiscovery.Schema,
		InstanceID:        encodedBytes(0x11, 32),
		ProcessID:         1,
		BaseURL:           origin,
		ControlCredential: credential,
		ExpiresAt:         time.Now().Add(time.Hour),
	}
}

func encodedBytes(fill byte, count int) string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{fill}, count))
}
