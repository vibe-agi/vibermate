package nativemcp_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/controlprincipal"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/nativemcp"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func recordedRouter(t *testing.T, runtime *productruntime.Runtime, repo nativetool.Repository, manuals manualcapture.Controller, clock *decisionClock) http.Handler {
	t.Helper()
	app, err := desktopcontrol.New(desktopcontrol.Options{Readiness: readyState(true), Status: runtime, Environments: runtime.Environments(), Assignments: runtime.CaptureAssignments(), Activities: runtime.Activities(), Contents: runtime.ExchangeContents(), Connections: runtime.ConnectionEvents(), Egress: runtime.EgressAttempts(), Approvals: runtime.ToolApprovals(), Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(), Offline: runtime, Clock: clock, ManualCaptures: manuals, NativeTools: repo})
	if err != nil {
		t.Fatal(err)
	}
	auth, err := desktopcontrol.NewAuthenticator(desktopcontrol.CapabilityGrant{ReadToken: capability(3), WriteToken: capability(4), ExpiresAt: time.Now().Add(time.Hour)}, desktopcontrol.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := controlprincipal.New(controlprincipal.Attributes{ID: "desktop:test", Kind: controlprincipal.KindDesktopApp, CredentialRevision: 1, AllowedGrantKinds: []controlprincipal.GrantKind{controlprincipal.GrantManualCapture}})
	if err != nil {
		t.Fatal(err)
	}
	unused := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
	router, err := desktopcontrol.NewRouter(desktopcontrol.RouterOptions{Authority: "127.0.0.1:43129", AllowedOrigins: []string{"vibermate://desktop"}, Authenticator: auth, Application: app, Bootstrap: unused, CLIControl: unused, ManualCaptures: manualHandler{}, DesktopPrincipal: principal})
	if err != nil {
		t.Fatal(err)
	}
	return router
}
func recordedGET(router http.Handler, path, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "http://127.0.0.1:43129"+path, nil)
	r.RemoteAddr = "127.0.0.1:50000"
	r.Header.Set("Origin", "vibermate://desktop")
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}
func recordedStatus(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("HTTP%d want%d: %s", w.Code, status, w.Body.String())
	}
	if status == 200 && w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("content cacheable")
	}
	for _, secret := range []string{"metadata-sentinel", "synthetic-original-login", "proof-private-sentinel"} {
		if bytes.Contains(w.Body.Bytes(), []byte(secret)) {
			t.Fatal("private field disclosed")
		}
	}
}

func TestNativeRecordedRouterImagesPagingAndCursorScope(t *testing.T) {
	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	var img bytes.Buffer
	if err := png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("汉🙂<&", 12000)
	result, _ := json.Marshal(map[string]any{"content": []any{map[string]any{"type": "text", "text": text}, map[string]any{"type": "image", "mimeType": "image/png", "data": base64.StdEncoding.EncodeToString(img.Bytes())}}, "structuredContent": map[string]any{"answer": 42}})
	nativemcp.ExerciseNativeRecordedControl(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingFull, RetentionDays: 1}, string(result), func(store *runtimepersistence.Store, manuals manualcapture.Controller, capture captureidentity.Reference, op nativetool.Operation) {
		clock := &decisionClock{time.Now().UTC()}
		router := recordedRouter(t, runtime, store.NativeToolRepository(), manuals, clock)
		path := "/api/v1/captures/" + capture.Key() + "/native-tools/operations/" + op.ID.String()
		recordedStatus(t, recordedGET(router, path, ""), 401)
		recordedStatus(t, recordedGET(router, path, capability(3)), 200)
		recordedStatus(t, recordedGET(router, strings.TrimSuffix(path, "/"+op.ID.String())+"?afterSequence=999999", capability(3)), 200)
		var firstCursor, cursor string
		var texts []string
		var imageData, structured string
		expectedOffset := 0
		for count := 0; ; count++ {
			if count > 100 {
				t.Fatal("cursor loop")
			}
			query := "?part=tool_result"
			if cursor != "" {
				query += "&cursor=" + url.QueryEscape(cursor)
			}
			w := recordedGET(router, path+"/content"+query, capability(3))
			recordedStatus(t, w, 200)
			if w.Body.Len() > 128<<10 {
				t.Fatal("encoded page exceeds bound")
			}
			var page desktopcontrol.NativeContentPageDTO
			if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
				t.Fatal(err)
			}
			if len(page.Blocks) > 24 || page.BodyDigest != op.Result.Digest.String() {
				t.Fatal("page scope/digest invalid")
			}
			for _, block := range page.Blocks {
				switch block.Kind {
				case "text":
					if block.Offset != expectedOffset {
						t.Fatal("text duplicated/omitted")
					}
					texts = append(texts, *block.Text)
					expectedOffset += len(*block.Text)
				case "image":
					imageData += *block.ImageData
				case "json":
					structured += *block.Text
				default:
					t.Fatal("untyped block")
				}
			}
			cursor = page.NextCursor
			if firstCursor == "" {
				firstCursor = cursor
			}
			if cursor == "" {
				break
			}
			if len(cursor) > 512 {
				t.Fatal("cursor exceeds limit")
			}
		}
		if strings.Join(texts, "") != text || imageData != base64.StdEncoding.EncodeToString(img.Bytes()) || structured != `{"answer":42}` || firstCursor == "" {
			t.Fatal("retained text/image/JSON changed")
		}
		arguments := recordedGET(router, path+"/content?part=tool_arguments", capability(3))
		recordedStatus(t, arguments, 200)
		if !strings.Contains(arguments.Body.String(), `frozen.txt`) || strings.Contains(arguments.Body.String(), `name`) {
			t.Fatal("arguments were rewrapped")
		}
		bad := firstCursor[:len(firstCursor)-1] + "!"
		recordedStatus(t, recordedGET(router, path+"/content?part=tool_result&cursor="+url.QueryEscape(bad), capability(3)), 422)
		signed, err := base64.RawURLEncoding.DecodeString(firstCursor)
		if err != nil {
			t.Fatal(err)
		}
		payload := bytes.Replace(signed[:len(signed)-32], []byte(op.Result.Digest.String()), []byte(strings.Repeat("0", 64)), 1)
		tampered := base64.RawURLEncoding.EncodeToString(append(payload, signed[len(signed)-32:]...))
		recordedStatus(t, recordedGET(router, path+"/content?part=tool_result&cursor="+url.QueryEscape(tampered), capability(3)), 422)
		recordedStatus(t, recordedGET(router, path+"/content?part=tool_arguments&cursor="+url.QueryEscape(firstCursor), capability(3)), 422)
		restarted := recordedRouter(t, runtime, store.NativeToolRepository(), manuals, clock)
		recordedStatus(t, recordedGET(restarted, path+"/content?part=tool_result&cursor="+url.QueryEscape(firstCursor), capability(3)), 422)
		other, err := manuals.Create(context.Background(), manualcapture.CreateCommand{Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "Other", ClientClass: manualcapture.ClientDesktopApp, Lifetime: manualcapture.LifetimeTemporary, ExpiresIn: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		recordedStatus(t, recordedGET(router, strings.Replace(path, capture.ID, other.Capture.ID, 1)+"/content?part=tool_result", capability(3)), 404)
		clock.now = clock.now.Add(301 * time.Second)
		recordedStatus(t, recordedGET(router, path+"/content?part=tool_result&cursor="+url.QueryEscape(firstCursor), capability(3)), 422)
	})
}

type deleteDuringNativeRead struct {
	nativetool.Repository
	remove func()
}

func (r *deleteDuringNativeRead) ReadContent(ctx context.Context, capture captureidentity.Reference, id nativetool.ID, part string, offset, limit int) ([]byte, error) {
	body, err := r.Repository.ReadContent(ctx, capture, id, part, offset, limit)
	if r.remove != nil {
		remove := r.remove
		r.remove = nil
		remove()
	}
	return body, err
}

func TestNativeRecordedRouterUnavailableAndDeleteDuringRead(t *testing.T) {
	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	for _, scenario := range []string{"off", "metadata_only", "unknown_profile", "invalid_codec", "corrupt_digest", "expired", "clear", "delete"} {
		t.Run(scenario, func(t *testing.T) {
			mode := environment.ContentRecordingFull
			if scenario == "off" {
				mode = environment.ContentRecordingOff
			}
			if scenario == "metadata_only" {
				mode = environment.ContentRecordingMetadataOnly
			}
			recording := environment.ContentRecordingPolicy{Mode: mode}
			if mode != environment.ContentRecordingOff {
				recording.RetentionDays = 1
			}
			result := `{"content":[{"type":"text","text":"retained body"}]}`
			if scenario == "invalid_codec" {
				result = `{"content":[{"type":"image","mimeType":"image/png","data":"ZmFrZQ=="}]}`
			}
			changes := []func(*sql.DB, nativetool.Operation){}
			if scenario == "corrupt_digest" {
				changes = append(changes, func(db *sql.DB, op nativetool.Operation) {
					if _, err := db.Exec(`UPDATE runtime_evidence_bodies SET plain_bytes=999 WHERE digest=?`, op.Result.Digest[:]); err != nil {
						t.Fatal(err)
					}
				})
			}
			exercise := nativemcp.ExerciseNativeRecordedControl
			if scenario == "unknown_profile" {
				exercise = nativemcp.ExerciseNativeUnknownRecordedControl
			}
			exercise(t, recording, result, func(store *runtimepersistence.Store, manuals manualcapture.Controller, capture captureidentity.Reference, op nativetool.Operation) {
				clock := &decisionClock{time.Now().UTC()}
				var repo nativetool.Repository = store.NativeToolRepository()
				if scenario == "expired" {
					clock.now = op.CreatedAt.Add(25 * time.Hour)
				}
				if scenario == "clear" || scenario == "delete" {
					repo = &deleteDuringNativeRead{Repository: repo, remove: func() {
						if scenario == "clear" {
							if _, err := store.ClearEvidence(context.Background()); err != nil {
								t.Fatal(err)
							}
						} else {
							if _, err := store.DeleteCapture(context.Background(), string(capture.Kind), capture.ID); err != nil {
								t.Fatal(err)
							}
						}
					}}
				}
				router := recordedRouter(t, runtime, repo, manuals, clock)
				path := "/api/v1/captures/" + capture.Key() + "/native-tools/operations/" + op.ID.String()
				metadata := recordedGET(router, path, capability(3))
				recordedStatus(t, metadata, 200)
				if !strings.Contains(metadata.Body.String(), `"state":"completed"`) {
					t.Fatal("missing body means running")
				}
				response := recordedGET(router, path+"/content?part=tool_result", capability(3))
				recordedStatus(t, response, 410)
				if strings.Contains(response.Body.String(), "retained body") {
					t.Fatal("invalid read disclosed body")
				}
			}, changes...)
		})
	}
}
