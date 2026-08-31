package messagetransform

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/clientannotation"
)

func TestTurnTransformsRequestAndResponseWithTransactionalSharedContext(t *testing.T) {
	t.Parallel()

	program, err := Compile(Policy{
		RequestJavaScript: `
			const payload = JSON.parse(request.body);
			context.originalModel = payload.model;
			request.headers["x-vibermate-test"] = ["request"];
			request.headers["content-length"] = ["1"];
			request.body = JSON.stringify({...payload, model: "opaque:upstream"});
		`,
		ResponseJavaScript: `
			const payload = JSON.parse(response.body);
			response.headers["x-vibermate-test"] = [context.originalModel];
			delete response.headers["x-remove-me"];
			response.body = JSON.stringify({...payload, requested_model: context.originalModel});
		`,
	}, DefaultLimits())
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	turn := program.NewTurn()

	request, err := turn.ApplyRequest(context.Background(), RequestMessage{
		Method:  http.MethodPost,
		Path:    "/v1/messages",
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body:    []byte(`{"model":"client-model","messages":[]}`),
	})
	if err != nil {
		t.Fatalf("ApplyRequest() error = %v", err)
	}
	if got := request.Headers.Get("X-Vibermate-Test"); got != "request" {
		t.Fatalf("request Header = %q, want request", got)
	}
	if got := request.Headers.Get("Content-Length"); got != "" {
		t.Fatalf("request Content-Length = %q, want Core-owned empty value", got)
	}
	if got := string(request.Body); got != `{"model":"opaque:upstream","messages":[]}` {
		t.Fatalf("request Body = %s", got)
	}
	if request.Method != http.MethodPost || request.Path != "/v1/messages" {
		t.Fatalf("request routing authority changed: %#v", request)
	}

	response, err := turn.ApplyResponse(context.Background(), ResponseMessage{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type": {"application/json"},
			"X-Remove-Me":  {"yes"},
		},
		Body: []byte(`{"id":"response-1"}`),
	})
	if err != nil {
		t.Fatalf("ApplyResponse() error = %v", err)
	}
	if got := response.Headers.Get("X-Vibermate-Test"); got != "client-model" {
		t.Fatalf("response shared Context Header = %q, want client-model", got)
	}
	if got := response.Headers.Get("X-Remove-Me"); got != "" {
		t.Fatalf("response deleted Header = %q, want empty", got)
	}
	if got := string(response.Body); got != `{"id":"response-1","requested_model":"client-model"}` {
		t.Fatalf("response Body = %s", got)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("response StatusCode = %d, want 200", response.StatusCode)
	}
}

func TestTurnFailsClosedAndDoesNotCommitContextOnScriptFailure(t *testing.T) {
	t.Parallel()

	program, err := Compile(Policy{
		RequestJavaScript:  `context.marker = "safe";`,
		ResponseJavaScript: `context.marker = "corrupt"; response.body = "changed"; throw new Error("stop");`,
	}, DefaultLimits())
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	turn := program.NewTurn()
	if _, err := turn.ApplyRequest(context.Background(), RequestMessage{
		Method: http.MethodPost, Path: "/v1/messages",
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body:    []byte(`{"ok":true}`),
	}); err != nil {
		t.Fatalf("ApplyRequest() error = %v", err)
	}
	original := ResponseMessage{
		StatusCode: http.StatusOK,
		Headers:    http.Header{"Content-Type": {"application/json"}},
		Body:       []byte(`{"unchanged":true}`),
	}
	if _, err := turn.ApplyResponse(context.Background(), original); !errors.Is(err, ErrExecutionFailed) {
		t.Fatalf("ApplyResponse() error = %v, want ErrExecutionFailed", err)
	}

	probe, err := Compile(Policy{ResponseJavaScript: `response.body = context.marker;`}, DefaultLimits())
	if err != nil {
		t.Fatalf("Compile(probe) error = %v", err)
	}
	probeTurn := probe.NewTurnWithContext(turn.ContextSnapshot())
	got, err := probeTurn.ApplyResponse(context.Background(), original)
	if err != nil {
		t.Fatalf("probe ApplyResponse() error = %v", err)
	}
	if string(got.Body) != "safe" {
		t.Fatalf("Context after failed transform = %q, want safe", got.Body)
	}
}

func TestCompileRejectsInvalidPolicyAndExecutionStopsWithRequestContext(t *testing.T) {
	t.Parallel()

	if _, err := Compile(Policy{RequestJavaScript: `if (`}, DefaultLimits()); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("Compile(invalid syntax) error = %v, want ErrInvalidPolicy", err)
	}
	if _, err := Compile(Policy{RequestJavaScript: "request.body = 'x'\x00;"}, DefaultLimits()); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("Compile(control character) error = %v, want ErrInvalidPolicy", err)
	}

	program, err := Compile(Policy{RequestJavaScript: `for (;;) {}`}, DefaultLimits())
	if err != nil {
		t.Fatalf("Compile(infinite loop) error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err = program.NewTurn().ApplyRequest(ctx, RequestMessage{
		Method: http.MethodPost, Path: "/v1/messages",
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body:    []byte(`{}`),
	})
	if !errors.Is(err, ErrExecutionFailed) || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("ApplyRequest(infinite loop) error = %v, want context-bounded ErrExecutionFailed", err)
	}
}

func TestExecutionStopsAtTheProgramLimitWithoutACallerDeadline(t *testing.T) {
	t.Parallel()

	limits := DefaultLimits()
	limits.MaximumExecutionDuration = 10 * time.Millisecond
	program, err := Compile(Policy{RequestJavaScript: `for (;;) {}`}, limits)
	if err != nil {
		t.Fatalf("Compile(infinite loop) error = %v", err)
	}
	started := time.Now()
	_, err = program.NewTurn().ApplyRequest(context.Background(), RequestMessage{
		Method: http.MethodPost, Path: "/v1/messages",
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body:    []byte(`{}`),
	})
	if !errors.Is(err, ErrExecutionFailed) {
		t.Fatalf("ApplyRequest(infinite loop) error = %v, want ErrExecutionFailed", err)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("ApplyRequest(infinite loop) elapsed = %v, want bounded execution", elapsed)
	}
}

func TestPipelineTurnExposesOneTrustedRuntimeSnapshotToBothStages(t *testing.T) {
	t.Parallel()

	pipeline, err := CompilePipeline([]Policy{{
		RequestJavaScript: `
			const payload = JSON.parse(request.body);
			if (payload.runtime?.user?.name === runtime.user.name) throw new Error("message forged runtime");
			context.originalHome = runtime.user.homeDirectory;
			request.headers["x-local-user"] = [runtime.user.name];
			runtime.user.name = "forged";
		`,
		ResponseJavaScript: `
			response.headers["x-runtime"] = [
				runtime.user.name,
				runtime.user.homeDirectory,
				runtime.device.operatingSystem,
				runtime.device.operatingSystemVersion,
				runtime.device.architecture,
				runtime.device.timeZone,
				runtime.workspace.root,
				runtime.workspace.label,
				context.originalHome,
			];
		`,
	}}, DefaultLimits())
	if err != nil {
		t.Fatalf("CompilePipeline() error = %v", err)
	}
	turn, err := pipeline.NewTurnWithMetadata(RuntimeMetadata{
		LocalUserName:          "jack",
		HomeDirectory:          "/Users/jack",
		OperatingSystem:        "darwin",
		OperatingSystemVersion: "15.6",
		Architecture:           "arm64",
		TimeZone:               "Asia/Singapore",
		WorkspaceRoot:          "/Users/jack/Code/vibermate",
		WorkspaceLabel:         "vibermate",
	})
	if err != nil {
		t.Fatalf("NewTurnWithMetadata() error = %v", err)
	}
	request, err := turn.ApplyRequest(context.Background(), RequestMessage{
		Method: http.MethodPost, Path: "/v1/messages",
		Headers: make(http.Header),
		Body:    []byte(`{"runtime":{"user":{"name":"mallory"}}}`),
	})
	if err != nil {
		t.Fatalf("ApplyRequest() error = %v", err)
	}
	if got := request.Headers.Get("X-Local-User"); got != "jack" {
		t.Fatalf("request runtime user = %q, want jack", got)
	}
	response, err := turn.ApplyResponse(context.Background(), ResponseMessage{
		StatusCode: http.StatusOK, Headers: make(http.Header), Body: []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("ApplyResponse() error = %v", err)
	}
	want := []string{
		"jack", "/Users/jack", "darwin", "15.6", "arm64", "Asia/Singapore",
		"/Users/jack/Code/vibermate", "vibermate", "/Users/jack",
	}
	if got := response.Headers.Values("X-Runtime"); !reflect.DeepEqual(got, want) {
		t.Fatalf("response runtime snapshot = %#v, want %#v", got, want)
	}
}

func TestPipelineIssuesStructuredAnnotationsAndRemovesThemBeforeTheNextRequest(t *testing.T) {
	t.Parallel()

	signer, err := clientannotation.NewSigner(bytes.Repeat([]byte{0x51}, 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(signer.Destroy)
	pipeline, err := CompilePipeline([]Policy{{
		RequestJavaScript: `
			const payload = JSON.parse(request.body);
			if (request.body.includes("vibermate:annotation")) throw new Error("annotation leaked");
			request.headers["x-clean-content"] = [payload.content];
		`,
		ResponseJavaScript: `
			const payload = JSON.parse(response.body);
			payload.content = runtime.annotations.create("turn-time", runtime.turn.startedAt);
			response.body = JSON.stringify(payload);
		`,
	}}, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	options := TurnOptions{
		Metadata:    RuntimeMetadata{TurnStartedAt: time.Date(2026, 8, 27, 6, 5, 4, 0, time.UTC)},
		Annotations: signer,
	}
	first, err := pipeline.NewTurnWithOptions(options)
	if err != nil {
		t.Fatal(err)
	}
	response, err := first.ApplyResponse(context.Background(), ResponseMessage{
		StatusCode: http.StatusOK,
		Headers:    http.Header{"Content-Type": {"application/json"}},
		Body:       []byte(`{"content":"answer"}`),
	})
	if err != nil {
		t.Fatalf("ApplyResponse() error = %v", err)
	}
	if !bytes.Contains(response.Body, []byte("2026-08-27T06:05:04Z")) ||
		!bytes.Contains(response.Body, []byte("vibermate:annotation")) {
		t.Fatalf("response Body has no visible structured annotation: %s", response.Body)
	}

	second, err := pipeline.NewTurnWithOptions(options)
	if err != nil {
		t.Fatal(err)
	}
	request, err := second.ApplyRequest(context.Background(), RequestMessage{
		Method:  http.MethodPost,
		Path:    "/v1/messages",
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body:    response.Body,
	})
	if err != nil {
		t.Fatalf("next ApplyRequest() error = %v", err)
	}
	if got := request.Headers.Get("X-Clean-Content"); got != "" {
		t.Fatalf("next request content = %q, want injected annotation removed", got)
	}
	if bytes.Contains(request.Body, []byte("vibermate:annotation")) ||
		bytes.Contains(request.Body, []byte("2026-08-27")) {
		t.Fatalf("next request retained client annotation: %s", request.Body)
	}
}

func TestTurnRejectsInvalidOutputsAndLeavesInputImmutable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		script string
	}{
		{name: "non UTF-8 body", script: `request.body = String.fromCharCode(0xd800);`},
		{name: "invalid Header value", script: `request.headers["x-test"] = ["bad\r\nvalue"];`},
		{name: "non JSON Context", script: `context.value = function () {};`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			program, err := Compile(Policy{RequestJavaScript: test.script}, DefaultLimits())
			if err != nil {
				t.Fatalf("Compile() error = %v", err)
			}
			input := RequestMessage{
				Method: http.MethodPost, Path: "/v1/messages",
				Headers: http.Header{"X-Original": {"kept"}},
				Body:    []byte(`{"original":true}`),
			}
			_, err = program.NewTurn().ApplyRequest(context.Background(), input)
			if !errors.Is(err, ErrInvalidOutput) {
				t.Fatalf("ApplyRequest() error = %v, want ErrInvalidOutput", err)
			}
			if got := input.Headers.Get("X-Original"); got != "kept" || string(input.Body) != `{"original":true}` {
				t.Fatalf("input mutated after failure: Header=%q Body=%s", got, input.Body)
			}
		})
	}
}

func TestPolicyLimitsScriptAndBodySizes(t *testing.T) {
	t.Parallel()

	limits := DefaultLimits()
	if _, err := Compile(Policy{RequestJavaScript: strings.Repeat(" ", limits.MaximumScriptBytes+1)}, limits); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("Compile(oversized) error = %v, want ErrInvalidPolicy", err)
	}
	program, err := Compile(Policy{RequestJavaScript: `request.body += "x";`}, limits)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	_, err = program.NewTurn().ApplyRequest(context.Background(), RequestMessage{
		Method: http.MethodPost, Path: "/v1/messages", Headers: make(http.Header),
		Body: []byte(strings.Repeat("x", limits.MaximumBodyBytes)),
	})
	if !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("ApplyRequest(oversized output) error = %v, want ErrInvalidOutput", err)
	}
}

func TestTurnExposesReadOnlyStreamingEventMetadata(t *testing.T) {
	t.Parallel()
	program, err := Compile(Policy{ResponseJavaScript: `
		if (!response.streaming || response.eventName !== "content_block_delta") {
			throw new Error("missing stream metadata");
		}
		response.streaming = false;
		response.eventName = "forged";
		const payload = JSON.parse(response.body);
		payload.delta.text = "changed";
		response.body = JSON.stringify(payload);
	`}, DefaultLimits())
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	response, err := program.NewTurn().ApplyResponse(context.Background(), ResponseMessage{
		StatusCode: http.StatusOK,
		Streaming:  true,
		EventName:  "content_block_delta",
		Headers:    http.Header{"Content-Type": {"text/event-stream"}},
		Body:       []byte(`{"delta":{"text":"original"}}`),
	})
	if err != nil {
		t.Fatalf("ApplyResponse() error = %v", err)
	}
	if !response.Streaming || response.EventName != "content_block_delta" {
		t.Fatalf("stream metadata was changed: %#v", response)
	}
	if got := string(response.Body); got != `{"delta":{"text":"changed"}}` {
		t.Fatalf("stream event Body = %s", got)
	}
}

func TestTurnRunsTheRequestStageOnceAcrossIdenticalInternalAttempts(t *testing.T) {
	t.Parallel()
	program, err := Compile(Policy{
		RequestJavaScript:  `context.requestRuns = (context.requestRuns ?? 0) + 1; request.headers["x-run"] = String(context.requestRuns);`,
		ResponseJavaScript: `response.body = String(context.requestRuns);`,
	}, DefaultLimits())
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	turn := program.NewTurn()
	input := RequestMessage{
		Method: http.MethodPost, Path: "/v1/messages",
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body:    []byte(`{"model":"client"}`),
	}
	first, err := turn.ApplyRequest(context.Background(), input)
	if err != nil {
		t.Fatalf("first ApplyRequest() error = %v", err)
	}
	second, err := turn.ApplyRequest(context.Background(), input)
	if err != nil {
		t.Fatalf("second ApplyRequest() error = %v", err)
	}
	if first.Headers.Get("X-Run") != "1" || second.Headers.Get("X-Run") != "1" {
		t.Fatalf("request stage ran more than once: first=%q second=%q", first.Headers.Get("X-Run"), second.Headers.Get("X-Run"))
	}
	response, err := turn.ApplyResponse(context.Background(), ResponseMessage{
		StatusCode: http.StatusOK, Headers: make(http.Header), Body: []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("ApplyResponse() error = %v", err)
	}
	if got := string(response.Body); got != "1" {
		t.Fatalf("request stage Context count = %q, want 1", got)
	}

	different := input
	different.Body = []byte(`{"model":"different"}`)
	if _, err := turn.ApplyRequest(context.Background(), different); !errors.Is(err, ErrExecutionFailed) {
		t.Fatalf("ApplyRequest(different retry) error = %v, want ErrExecutionFailed", err)
	}
}

func TestPipelineRunsRequestForwardAndResponseReverseWithIsolatedContext(t *testing.T) {
	t.Parallel()

	pipeline, err := CompilePipeline([]Policy{
		{
			RequestJavaScript:  `context.name = "A"; request.body += context.name;`,
			ResponseJavaScript: `response.body += context.name;`,
		},
		{
			RequestJavaScript: `
				if (context.name !== undefined) throw new Error("shared context");
				context.name = "B";
				request.body += context.name;
			`,
			ResponseJavaScript: `response.body += context.name;`,
		},
	}, DefaultLimits())
	if err != nil {
		t.Fatalf("CompilePipeline() error = %v", err)
	}
	turn := pipeline.NewTurn()
	input := RequestMessage{
		Method: http.MethodPost, Path: "/v1/messages",
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body:    []byte(""),
	}
	request, err := turn.ApplyRequest(context.Background(), input)
	if err != nil {
		t.Fatalf("ApplyRequest() error = %v", err)
	}
	retried, err := turn.ApplyRequest(context.Background(), input)
	if err != nil {
		t.Fatalf("retried ApplyRequest() error = %v", err)
	}
	if got := string(request.Body); got != "AB" || string(retried.Body) != "AB" {
		t.Fatalf("request pipeline Body = %q, retry = %q, want AB", request.Body, retried.Body)
	}

	response, err := turn.ApplyResponse(context.Background(), ResponseMessage{
		StatusCode: http.StatusOK,
		Headers:    http.Header{"Content-Type": {"application/json"}},
		Body:       []byte(""),
	})
	if err != nil {
		t.Fatalf("ApplyResponse() error = %v", err)
	}
	if got := string(response.Body); got != "BA" {
		t.Fatalf("response pipeline Body = %q, want BA", got)
	}
}
