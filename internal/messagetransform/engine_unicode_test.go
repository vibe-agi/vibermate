package messagetransform

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"
)

func TestTurnPreservesValidUnicodeBody(t *testing.T) {
	t.Parallel()
	for _, body := range []string{"�", "中文 � 🙂", "\ufffd\ufffd", "\U00010000\U0010ffff"} {
		for _, saveContext := range []bool{false, true} {
			t.Run(body+map[bool]string{false: "/headers-only", true: "/context"}[saveContext], func(t *testing.T) {
				requestScript := `request.headers["x-test"] = "changed";`
				responseScript := `response.headers["x-test"] = "changed";`
				if saveContext {
					requestScript += `context.body = request.body;`
					responseScript += `response.body = context.body;`
				}
				program, err := Compile(Policy{RequestJavaScript: requestScript, ResponseJavaScript: responseScript}, DefaultLimits())
				if err != nil {
					t.Fatal(err)
				}
				turn := program.NewTurn()
				input := RequestMessage{Method: http.MethodPost, Path: "/v1/responses", Body: []byte(body)}
				request, err := turn.ApplyRequest(context.Background(), input)
				if err != nil || !bytes.Equal(request.Body, input.Body) {
					t.Fatalf("valid request body changed or rejected: %q, %v", request.Body, err)
				}
				retry, err := turn.ApplyRequest(context.Background(), input)
				if err != nil || !reflect.DeepEqual(request, retry) {
					t.Fatalf("retry changed valid Unicode: %+v, %v", retry, err)
				}
				for _, streaming := range []bool{false, true} {
					response, err := turn.ApplyResponse(context.Background(), ResponseMessage{
						StatusCode: http.StatusOK, Streaming: streaming, Body: []byte(body),
					})
					if err != nil || string(response.Body) != body {
						t.Fatalf("valid response body changed or rejected (streaming=%v): %q, %v", streaming, response.Body, err)
					}
				}
			})
		}
	}
}

func TestTurnDistinguishesReplacementCharacterFromUnpairedSurrogates(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, expression string
		valid            bool
	}{
		{"replacement", `"\ufffd"`, true},
		{"surrogate-pair", `"\ud83d\ude42"`, true},
		{"replacement-and-pair", `"\ufffd\ud83d\ude42"`, true},
		{"high-surrogate", `"\ud800"`, false},
		{"low-surrogate", `"\udfff"`, false},
		{"reversed-pair", `"\udc00\ud800"`, false},
		{"double-high", `"\ud800\ud800"`, false},
		{"high-then-ascii", `"\ud800x"`, false},
		{"pair-then-high", `"\ud83d\ude42\ud800"`, false},
		{"replacement-then-high", `"\ufffd\ud800"`, false},
	} {
		for _, target := range []string{"request.body", "response.body", "context.value"} {
			t.Run(test.name+"/"+target, func(t *testing.T) {
				policy := Policy{RequestJavaScript: target + " = " + test.expression + ";"}
				if target == "response.body" {
					policy = Policy{ResponseJavaScript: policy.RequestJavaScript}
				}
				program, err := Compile(policy, DefaultLimits())
				if err != nil {
					t.Fatal(err)
				}
				turn := program.NewTurn()
				before := turn.ContextSnapshot()
				if target == "response.body" {
					_, err = turn.ApplyResponse(context.Background(), ResponseMessage{StatusCode: http.StatusOK, Body: []byte("original")})
				} else {
					_, err = turn.ApplyRequest(context.Background(), RequestMessage{Method: http.MethodPost, Body: []byte("original")})
				}
				if test.valid {
					if err != nil {
						t.Fatalf("valid Unicode rejected: %v", err)
					}
				} else if !errors.Is(err, ErrInvalidOutput) || !reflect.DeepEqual(before, turn.ContextSnapshot()) {
					t.Fatalf("invalid UTF-16 accepted or committed Context: %v", err)
				}
			})
		}
	}
}

func TestTurnStillRejectsInvalidUTF8Input(t *testing.T) {
	program, err := Compile(Policy{RequestJavaScript: `request.headers["x-test"] = "changed";`, ResponseJavaScript: `response.body = response.body;`}, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range [][]byte{{0xff}, {0xed, 0xa0, 0x80}, {0xef, 0xbf}} {
		turn := program.NewTurn()
		if _, err := turn.ApplyRequest(context.Background(), RequestMessage{Method: http.MethodPost, Body: body}); !errors.Is(err, ErrInvalidOutput) {
			t.Fatalf("invalid request UTF-8 accepted: %v", err)
		}
		if _, err := turn.ApplyResponse(context.Background(), ResponseMessage{StatusCode: http.StatusOK, Body: body}); !errors.Is(err, ErrInvalidOutput) {
			t.Fatalf("invalid response UTF-8 accepted: %v", err)
		}
	}
}
