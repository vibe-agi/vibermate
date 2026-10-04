package nativemcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/environment"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestRecordedContentExactRetainedShapes(t *testing.T) {
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(pngBytes.Bytes())
	for _, tc := range []struct{ part, body, kind, text string }{
		{"tool_arguments", `{"path":"frozen.txt"}`, "json", `{"path":"frozen.txt"}`},
		{"tool_result", "{\"content\":[{\"type\":\"text\",\"text\":\"\u771f\u5b9e text\"}]}", "text", "\u771f\u5b9e text"},
		{"tool_result", `{"structuredContent":{"answer":42}}`, "json", `{"answer":42}`},
		{"tool_result", `{"content":[{"type":"image","mimeType":"image/png","data":"` + encoded + `"}]}`, "image", ""},
	} {
		got, err := DecodeRecordedToolContent(tc.part, []byte(tc.body))
		if err != nil || len(got.Blocks) != 1 || got.Blocks[0].Kind != tc.kind || got.Blocks[0].Text != tc.text {
			t.Fatalf("%s: %+v %v", tc.part, got, err)
		}
		if tc.kind == "image" && (got.Blocks[0].MIMEType != "image/png" || !bytes.Equal(got.Blocks[0].ImageBytes, pngBytes.Bytes())) {
			t.Fatal("image bytes changed")
		}
	}
}

func TestRecordedNestedMetadataIsNotRetained(t *testing.T) {
	f := newNativeFixtureMode(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingFull, RetentionDays: 1})
	f.executor.(*executor).dependencies.ContentProfiles = syntheticProfiles{}
	f.handshake(t)
	f.transport.handler = func(r *http.Request, body []byte) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"public","_meta":{"proof":"proof-private-sentinel"}}]}}`))}, nil
	}
	call := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"}}}`, "synthetic-session")
	terminal := f.executor.Serve(context.Background(), call, httptest.NewRecorder())
	if terminal.Outcome != OutcomeCompleted {
		t.Fatal(terminal)
	}
	op, err := f.store.NativeToolRepository().Get(context.Background(), call.id)
	if err != nil {
		t.Fatal(err)
	}
	if op.Result == nil {
		t.Fatal("public result suppressed with private metadata")
	}
	body, err := f.store.NativeToolRepository().ReadContent(context.Background(), f.capture, call.id, "tool_result", 0, 128<<10)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeRecordedToolContent("tool_result", body)
	if err != nil || len(decoded.Blocks) != 1 || decoded.Blocks[0].Text != "public" {
		t.Fatalf("public result lost %+v %v", decoded, err)
	}
	assertNoSensitiveRows(t, f, []string{"proof-private-sentinel"})
}

func TestRecordedBusinessMetadataFidelityAndExplicitSensitivePaths(t *testing.T) {
	for _, sensitive := range []bool{false, true} {
		t.Run(fmt.Sprint(sensitive), func(t *testing.T) {
			f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingFull, RetentionDays: 1})
			e := f.executor.(*executor)
			profiles := syntheticProfiles{}
			if sensitive {
				profiles.argumentPaths = []string{"/_meta"}
				profiles.resultPaths = []string{"/structuredContent/_meta"}
			}
			e.dependencies.ContentProfiles = profiles
			f.handshake(t)
			result := `{"content":[{"type":"text","text":"public"}],"structuredContent":{"_meta":{"business":"kept"},"nested":{"_meta":1}}}`
			f.transport.handler = func(r *http.Request, body []byte) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":3,"result":` + result + `}`))}, nil
			}
			call := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a","_meta":{"business":"kept"},"nested":{"_meta":1}},"_meta":{"proof":"transport-private-sentinel"}}}`, "synthetic-session")
			results := make(chan Terminal, 1)
			go func() { results <- e.Serve(context.Background(), call, httptest.NewRecorder()) }()
			approval := waitNativeQuestion(t, f, call.id)
			preview, err := e.PendingPreview(context.Background(), f.capture, call.id, approval.BindingDigest)
			if sensitive {
				if err == nil {
					t.Error("explicit sensitive path previewed")
				}
			} else {
				if err != nil || len(preview.Intents) != 1 || !bytes.Contains(preview.Intents[0].Arguments, []byte(`"_meta":{"business":"kept"}`)) || !bytes.Contains(preview.Intents[0].Arguments, []byte(`"nested":{"_meta":1}`)) {
					t.Errorf("business preview altered %+v %v", preview, err)
				}
			}
			f.approve(t, call.id)
			if terminal := <-results; terminal.Outcome != OutcomeCompleted {
				t.Fatal(terminal)
			}
			op, err := f.store.NativeToolRepository().Get(context.Background(), call.id)
			if err != nil {
				t.Fatal(err)
			}
			if sensitive {
				if op.Arguments != nil || op.Result != nil {
					t.Fatal("explicit sensitive body/digest retained")
				}
			} else {
				for _, part := range []string{"tool_arguments", "tool_result"} {
					body, err := f.store.NativeToolRepository().ReadContent(context.Background(), f.capture, call.id, part, 0, 128<<10)
					if err != nil {
						t.Fatal(err)
					}
					decoded, err := DecodeRecordedToolContent(part, body)
					if err != nil {
						t.Errorf("business metadata refused %+v %v", decoded, err)
					}
					if !bytes.Contains(body, []byte(`"_meta":{"business":"kept"}`)) || !bytes.Contains(body, []byte(`"nested":{"_meta":1}`)) {
						t.Errorf("business bytes altered %s", part)
					}
				}
			}
			assertNoSensitiveRows(t, f, []string{"transport-private-sentinel"})
		})
	}
}

func TestRecordedSensitiveJSONPointerArraysAndEscapedKeys(t *testing.T) {
	for _, tc := range []struct{ name, path, args, result string }{
		{"array", "/items/0/proof", `{"path":"a","items":[{"proof":"array-private-sentinel"}]}`, `{"structuredContent":{"items":[{"proof":"array-private-sentinel"}]}}`},
		{"numeric_object_key", "/items/0/proof", `{"path":"a","items":{"0":{"proof":"array-private-sentinel"}}}`, `{"structuredContent":{"items":{"0":{"proof":"array-private-sentinel"}}}}`},
		{"escaped_object_keys", "/a~1b/~0proof", `{"path":"a","a/b":{"~proof":"array-private-sentinel"}}`, `{"structuredContent":{"a/b":{"~proof":"array-private-sentinel"}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingFull, RetentionDays: 1})
			e := f.executor.(*executor)
			e.dependencies.ContentProfiles = syntheticProfiles{argumentPaths: []string{tc.path}, resultPaths: []string{"/structuredContent" + tc.path}}
			f.handshake(t)
			f.transport.handler = func(r *http.Request, body []byte) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":3,"result":` + tc.result + `}`))}, nil
			}
			call := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":`+tc.args+`}}`, "synthetic-session")
			results := make(chan Terminal, 1)
			go func() { results <- e.Serve(context.Background(), call, httptest.NewRecorder()) }()
			a := waitNativeQuestion(t, f, call.id)
			if _, err := e.PendingPreview(context.Background(), f.capture, call.id, a.BindingDigest); err == nil {
				t.Error("explicit sensitive JSON pointer previewed")
			}
			f.approve(t, call.id)
			if terminal := <-results; terminal.Outcome != OutcomeCompleted {
				t.Fatal(terminal)
			}
			op, err := f.store.NativeToolRepository().Get(context.Background(), call.id)
			if err != nil {
				t.Fatal(err)
			}
			if op.Arguments != nil || op.Result != nil {
				t.Error("explicit sensitive JSON pointer body/digest retained")
			}
			assertNoSensitiveRows(t, f, []string{"array-private-sentinel"})
		})
	}
}

func TestRecordedImageSafety(t *testing.T) {
	// Go x/image v0.45.0 BSD test image: blue-purple-pink.lossy.webp.
	fixture, err := os.ReadFile("testdata/recorded-webp.base64")
	if err != nil {
		t.Fatal(err)
	}
	webp, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(fixture)))
	if err != nil {
		t.Fatal(err)
	}
	var jpegBytes bytes.Buffer
	if err = jpeg.Encode(&jpegBytes, image.NewRGBA(image.Rect(0, 0, 2, 3)), nil); err != nil {
		t.Fatal(err)
	}
	check := func(mime string, data []byte, want bool) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"content": []any{map[string]any{"type": "image", "mimeType": mime, "data": base64.StdEncoding.EncodeToString(data)}}})
		got, err := DecodeRecordedToolContent("tool_result", body)
		if (err == nil) != want {
			t.Fatalf("%s valid=%v got %+v %v", mime, want, got, err)
		}
	}
	check("image/webp", webp, true)
	check("image/jpeg", jpegBytes.Bytes(), true)
	check("image/png", webp, false)
	check("image/webp", webp[:20], false)
	// Small VP8X canvas must not permit a contradictory larger VP8 payload.
	chunk := append([]byte{'V', 'P', '8', 'X', 10, 0, 0, 0}, make([]byte, 10)...)
	attack := append([]byte("RIFF"), make([]byte, 4)...)
	attack = append(attack, []byte("WEBP")...)
	attack = append(attack, chunk...)
	attack = append(attack, webp[12:]...)
	binary.LittleEndian.PutUint32(attack[4:8], uint32(len(attack)-8))
	check("image/webp", attack, false)
	check("image/webp", bytes.Repeat([]byte{0}, (4<<20)+1), false)
	// VP8 frame dimension field independent of VP8X: 8192 x 8192.
	bomb := bytes.Clone(webp)
	binary.LittleEndian.PutUint16(bomb[26:28], 8192)
	binary.LittleEndian.PutUint16(bomb[28:30], 8192)
	check("image/webp", bomb, false)
}

func TestRecordedResourceNeverFetchesURI(t *testing.T) {
	got, err := DecodeRecordedToolContent("tool_result", []byte(`{"content":[{"type":"resource_link","uri":"https://private.example/secret?token=proof","name":"report"}]}`))
	if err != nil || len(got.Blocks) != 1 || got.Blocks[0].Kind != "text" || got.Blocks[0].Text != "Resource unavailable" {
		t.Fatalf("resource %+v %v", got, err)
	}
}

func TestRecordedContentRefusesInvalidBytes(t *testing.T) {
	for _, body := range [][]byte{
		[]byte(`{"content":[{"type":"image","mimeType":"image/png","data":"ZmFrZQ=="}]}`),
		[]byte(`{"content":[{"type":"image","mimeType":"image/svg+xml","data":"PHN2Zy8+"}]}`),
		[]byte(`{"content":[{"type":"text","text":"x"}],"_meta":{"proof":"secret"}}`),
		[]byte(`{"content":[{"type":"text","text":"x","_meta":{"proof":"secret"}}]}`),
		[]byte(`{"content":[],"content":[]}`),
		[]byte(`{"content":[{"type":"text","text":null}]}`),
		[]byte(`{"content":[],"credentialId":"secret"}`),
		{0xff}, bytes.Repeat([]byte("x"), (16<<20)+1),
	} {
		if got, err := DecodeRecordedToolContent("tool_result", body); err == nil {
			t.Fatalf("unsafe decoded %+v", got)
		}
	}
	if _, err := DecodeRecordedToolContent("request", []byte(`{}`)); err == nil {
		t.Fatal("arbitrary part accepted")
	}
}
