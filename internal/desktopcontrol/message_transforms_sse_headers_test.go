package desktopcontrol

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/messagetransform"
	"github.com/vibe-agi/vibermate/internal/ssewire"
)

func sseHeaderSample(headers http.Header, body, script string) MessageTransformTestInput {
	return MessageTransformTestInput{
		WireProtocol: transformProtocolOpenAIResponses,
		Policy:       messagetransform.Policy{ResponseJavaScript: script},
		Sample: &MessageTransformTestSample{
			Request:  MessageTransformTestRequest{Method: "POST", Path: "/v1/responses", Headers: http.Header{"content-type": {"application/json"}}, Body: `{"input":"sample","stream":true}`},
			Response: MessageTransformTestResponse{StatusCode: 200, Streaming: true, Headers: headers, Body: body},
		},
	}
}

const headerProbeStream = "event: probe\nid: one\ndata: {\"text\":\"private one\"}\n\n" +
	"event: probe\nid: two\ndata: {\"text\":\"private two\"}\n\n" +
	"data: [DONE]\n\n"

func TestMessageTransformSSEHeadersAndDone(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"content-type", "Content-Type", "CONTENT-TYPE", "cOnTeNt-TyPe"} {
		for _, edit := range []string{"unchanged", "add", "remove", "replace"} {
			t.Run(name+"/"+edit, func(t *testing.T) {
				t.Parallel()
				headers := http.Header{name: {"text/event-stream"}, "x-remove": {"before"}, "content-length": {"999"}, "etag": {"old"}}
				original := headers.Clone()
				script := `
					context.events = (context.events || 0) + 1;
					const payload = JSON.parse(response.body);
					payload.text = payload.text.replace("private", "sanitized");
					payload.sequence = context.events;
					response.body = JSON.stringify(payload);
				`
				switch edit {
				case "add":
					script += `response.headers["x-added"] = ["stable", "ordered"];`
				case "remove":
					script += `delete response.headers["x-remove"];`
				case "replace":
					script += `response.headers["x-remove"] = ["after"];`
				}
				result, err := runMessageTransformSample(context.Background(), sseHeaderSample(headers, headerProbeStream, script))
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(headers, original) || !reflect.DeepEqual(result.ResponseBefore.Headers, original) || result.ResponseBefore.Body != headerProbeStream {
					t.Fatal("input snapshot or caller headers mutated")
				}
				if result.ResponseAfter.StatusCode != 200 || !result.ResponseAfter.Streaming || result.ResponseAfter.Headers.Get("Content-Type") != "text/event-stream" {
					t.Fatal("response envelope changed")
				}
				if result.ResponseAfter.Headers.Get("Content-Length") != "" || result.ResponseAfter.Headers.Get("ETag") != "" {
					t.Fatal("DONE restored removed framing or representation validators")
				}
				if edit == "add" && !reflect.DeepEqual(result.ResponseAfter.Headers.Values("X-Added"), []string{"stable", "ordered"}) {
					t.Fatal("DONE lost added response header")
				}
				wantRemove := "before"
				if edit == "remove" {
					wantRemove = ""
				}
				if edit == "replace" {
					wantRemove = "after"
				}
				if result.ResponseAfter.Headers.Get("X-Remove") != wantRemove {
					t.Fatal("DONE reset transformed response header")
				}
				decoder, err := ssewire.NewDecoder(ssewire.DefaultOptions())
				if err != nil {
					t.Fatal(err)
				}
				events, err := decoder.Feed([]byte(result.ResponseAfter.Body))
				if err != nil {
					t.Fatal(err)
				}
				if err := decoder.Finish(); err != nil {
					t.Fatal(err)
				}
				if len(events) != 3 || events[0].Name != "probe" || events[1].Name != "probe" || string(events[2].Data) != "[DONE]" {
					t.Fatal("SSE event count/name or DONE changed")
				}
				if !strings.Contains(string(events[0].Data), `"sequence":1`) || !strings.Contains(string(events[1].Data), `"sequence":2`) || strings.Contains(result.ResponseAfter.Body, "private") {
					t.Fatal("event transformation or context sequence changed")
				}
				if !strings.Contains(result.ResponseAfter.Body, "id: one\n") || !strings.Contains(result.ResponseAfter.Body, "id: two\n") {
					t.Fatal("SSE event IDs changed")
				}
			})
		}
	}
}

func TestMessageTransformSSEStillRejectsHeaderChanges(t *testing.T) {
	t.Parallel()
	for name, mutation := range map[string]string{
		"value":       `response.headers["x-order"] = [String(context.events)];`,
		"addition":    `if (context.events > 1) response.headers["x-added"] = ["new"];`,
		"removal":     `if (context.events > 1) delete response.headers["x-order"];`,
		"value_order": `response.headers["x-order"] = context.events === 1 ? ["a", "b"] : ["b", "a"];`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			script := `context.events = (context.events || 0) + 1;` + mutation
			_, err := runMessageTransformSample(context.Background(), sseHeaderSample(http.Header{"content-type": {"text/event-stream"}, "x-order": {"original"}}, headerProbeStream, script))
			if err == nil || err.Error() != "streaming response transform changed Headers after the first event" {
				t.Fatalf("wanted real midstream header change rejection, got %v", err)
			}
		})
	}
}

func TestMessageTransformSSEDoneOnlySkipsJavaScript(t *testing.T) {
	t.Parallel()
	for name, headers := range map[string]http.Header{"nil": nil, "empty": {}, "lowercase": {"content-type": {"text/event-stream"}}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			result, err := runMessageTransformSample(context.Background(), sseHeaderSample(headers, "data: [DONE]\n\n", `throw new Error("DONE must not invoke JS");`))
			if err != nil {
				t.Fatal(err)
			}
			if result.ResponseAfter.Body != "data: [DONE]\n\n" || !reflect.DeepEqual(result.ResponseAfter.Headers, headers) {
				t.Fatal("terminal-only response changed")
			}
		})
	}
}

func TestMessageTransformTestHeadersEqual(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		left, right http.Header
		want        bool
	}{
		{"nil_empty", nil, http.Header{}, true},
		{"mixed_names", http.Header{"Content-Type": {"text/event-stream"}, "X-A": {"1", "2"}}, http.Header{"content-type": {"text/event-stream"}, "x-a": {"1", "2"}}, true},
		{"uppercase", http.Header{"CONTENT-TYPE": {"text/event-stream"}}, http.Header{"cOnTeNt-TyPe": {"text/event-stream"}}, true},
		{"values_case_sensitive", http.Header{"X-A": {"Value"}}, http.Header{"x-a": {"value"}}, false},
		{"values_ordered", http.Header{"X-A": {"a", "b"}}, http.Header{"x-a": {"b", "a"}}, false},
		{"different_field", http.Header{"X-A": {"a"}}, http.Header{"x-b": {"a"}}, false},
		{"missing_empty_field", http.Header{"X-A": nil}, http.Header{}, false},
		{"different_nil_field", http.Header{"X-A": nil}, http.Header{"X-B": nil}, false},
		{"extra_value", http.Header{"X-A": {"a"}}, http.Header{"x-a": {"a", "b"}}, false},
		{"duplicate_case_names", http.Header{"X-A": {"a"}, "x-a": {"a"}}, http.Header{"X-A": {"a"}, "X-B": {"a"}}, false},
		{"duplicate_both_sides", http.Header{"X-A": {"a"}, "x-a": {"a"}}, http.Header{"X-A": {"a"}, "x-a": {"a"}}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			left, right := test.left.Clone(), test.right.Clone()
			if got := messageTransformTestHeadersEqual(test.left, test.right); got != test.want {
				t.Fatalf("forward = %v, want %v", got, test.want)
			}
			if got := messageTransformTestHeadersEqual(test.right, test.left); got != test.want {
				t.Fatalf("reverse = %v, want %v", got, test.want)
			}
			if !reflect.DeepEqual(left, test.left) || !reflect.DeepEqual(right, test.right) {
				t.Fatal("comparison mutated its input")
			}
		})
	}
}

func TestMessageTransformSSELogicalHeaderBoundary(t *testing.T) {
	t.Parallel()
	for _, spelling := range []string{"lower", "upper", "canonical"} {
		for _, mode := range []string{"no_op", "late_edit", "done_only", "no_response_script"} {
			t.Run(spelling+"/"+mode, func(t *testing.T) {
				t.Parallel()
				headers := http.Header{"content-type": {"text/event-stream"}, "x-probe": {"stable"}}
				for _, name := range []string{"content-encoding", "content-length", "content-md5", "digest", "etag", "transfer-encoding"} {
					switch spelling {
					case "upper":
						name = strings.ToUpper(name)
					case "canonical":
						name = http.CanonicalHeaderKey(name)
					}
					headers[name] = []string{"upstream-representation-only"}
				}
				original := headers.Clone()
				body := "data: {\"type\":\"metadata\"}\n\n" +
					"data: {\"text\":\"private later\"}\n\n" +
					"data: [DONE]\n\n"
				script := `void 0;`
				switch mode {
				case "late_edit":
					script = `
						const payload = JSON.parse(response.body);
						if (typeof payload.text === "string") {
							payload.text = payload.text.replace("private", "sanitized");
							response.body = JSON.stringify(payload);
						}
					`
				case "done_only":
					body = "data: [DONE]\n\n"
					script = `throw new Error("DONE must not invoke JavaScript");`
				case "no_response_script":
					script = ""
				}
				result, err := runMessageTransformSample(context.Background(), sseHeaderSample(headers, body, script))
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(headers, original) || !reflect.DeepEqual(result.ResponseBefore.Headers, original) || result.ResponseBefore.Body != body {
					t.Fatal("logical header preparation mutated the original sample")
				}
				if mode == "no_response_script" {
					if !reflect.DeepEqual(result.ResponseAfter.Headers, original) || result.ResponseAfter.Body != body {
						t.Fatal("response without JavaScript changed")
					}
					return
				}
				for name := range result.ResponseAfter.Headers {
					if !strings.EqualFold(name, "content-type") && !strings.EqualFold(name, "x-probe") {
						t.Fatalf("obsolete upstream field survived logical stream preparation: %s", name)
					}
				}
				if len(result.ResponseAfter.Headers) != 2 {
					t.Fatal("unrelated header was lost")
				}
				want := body
				if mode == "late_edit" {
					want = strings.ReplaceAll(body, "private", "sanitized")
				}
				if result.ResponseAfter.Body != want {
					t.Fatal("logical header preparation changed stream data")
				}
			})
		}
	}
}
