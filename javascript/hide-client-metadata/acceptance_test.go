package clientmetadata_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/messagetransform"
)

func configuredMetadataSource(t *testing.T, values map[string]any) string {
	t.Helper()
	source := requestSource
	defaults := map[string]string{"version": "0.0.0", "installId": sampleInstallID, "userAgent": sampleUserAgent}
	for field, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		previous := field + `: "` + defaults[field] + `"`
		if strings.Count(source, previous) != 1 {
			t.Fatalf("configuration location is ambiguous: %s", field)
		}
		// JSON permits raw DEL, but the source policy rejects it before JS runs.
		// Use an escape so this fixture exercises the runtime value validator.
		literal := strings.ReplaceAll(string(encoded), "\x7f", `\u007f`)
		source = strings.Replace(source, previous, field+": "+literal, 1)
	}
	return source
}

func TestAcceptanceMetadataExactLengthBoundaries(t *testing.T) {
	for _, test := range []struct{ name, field, value, header string }{
		{"version-64", "version", strings.Repeat("1", 60) + ".0.0", "Version"},
		{"installation-128", "installId", strings.Repeat("a", 128), "X-Install-Id"},
		{"agent-512", "userAgent", strings.Repeat("A", 512), "User-Agent"},
		{"installation-one", "installId", "a", "X-Install-Id"},
		{"agent-one", "userAgent", "A", "User-Agent"},
		{"version-prerelease", "version", "1.2.3-beta.1", "Version"},
		{"version-build", "version", "1.2.3+fixture.1", "Version"},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := messagetransform.RequestMessage{Method: "POST", Path: "/v1/responses", Body: []byte("{}"), Headers: http.Header{test.header: {"original"}}}
			source := configuredMetadataSource(t, map[string]any{test.field: test.value})
			output, err := compile(t, source).NewTurn().ApplyRequest(context.Background(), input)
			if err != nil || output.Headers.Get(test.header) != test.value {
				t.Fatalf("valid boundary rejected or altered: %v", err)
			}
		})
	}
	for _, test := range []struct{ name, field, value string }{
		{"version-65", "version", strings.Repeat("1", 61) + ".0.0"},
		{"installation-129", "installId", strings.Repeat("a", 129)},
		{"agent-513", "userAgent", strings.Repeat("A", 513)},
		{"valid-version-with-LF", "version", "1.2.3\n"},
		{"valid-install-with-LF", "installId", "fixture\n"},
		{"valid-agent-with-LF", "userAgent", "fixture/1.0\n"},
		{"agent-DEL", "userAgent", "fixture\x7f"},
		{"agent-NUL", "userAgent", "fixture\x00"},
		{"agent-leading-space", "userAgent", " fixture/1.0"},
		{"agent-trailing-space", "userAgent", "fixture/1.0 "},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := configuredMetadataSource(t, map[string]any{test.field: test.value})
			_, err := compile(t, source).NewTurn().ApplyRequest(context.Background(), messagetransform.RequestMessage{Method: "POST", Path: "/v1/responses", Body: []byte("{}")})
			if err == nil {
				t.Fatal("invalid boundary accepted")
			}
		})
	}
}

func TestAcceptanceMetadataAllDisabled(t *testing.T) {
	source := configuredMetadataSource(t, map[string]any{"version": nil, "installId": nil, "userAgent": nil})
	for _, headers := range []http.Header{nil, {}, {"Version": {"9.8.7"}, "X-Install-Id": {"original"}, "User-Agent": {"original/9.8.7"}}} {
		body := []byte(`{"input":"do not change","account_id":"fixture"}`)
		output, err := compile(t, source).NewTurn().ApplyRequest(context.Background(), messagetransform.RequestMessage{Method: "POST", Path: "/v1/responses", Headers: headers, Body: body})
		if err != nil || len(output.Headers) != len(headers) || !bytes.Equal(output.Body, body) {
			t.Fatalf("disabled script changed the envelope: %v", err)
		}
		for name, values := range headers {
			if !reflect.DeepEqual(output.Headers.Values(name), values) {
				t.Fatalf("disabled script changed %s", name)
			}
		}
	}
}

func TestAcceptanceMetadataIndependentTurnsAreIdempotent(t *testing.T) {
	pipeline := compile(t, requestSource)
	input := messagetransform.RequestMessage{Method: "POST", Path: "/v1/responses", Body: []byte(`{"version":"body-not-a-header"}`), Headers: http.Header{
		"Version": {"9.8.7"}, "X-Install-Id": {"fixture"}, "User-Agent": {"fixture/9.8.7"}, "Session-Id": {"keep-session"},
	}}
	first, err := pipeline.NewTurn().ApplyRequest(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	for round := range 20 {
		next, err := pipeline.NewTurn().ApplyRequest(context.Background(), first)
		if err != nil || !reflect.DeepEqual(first, next) {
			t.Fatalf("independent round %d changed already normalized data: %v", round, err)
		}
	}
}

func TestAcceptanceMetadataLargeUnicodeBodyIsByteIdentical(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"input":    strings.Repeat("中文 café 🚀 \\ \" /Users/fixture/project 1.2.3 ", 24000),
		"metadata": map[string]string{"version": "9.8.7", "install_id": "leave-body-alone", "user_agent": "leave-body-alone"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(body) < 1<<20 {
		t.Fatal("fixture does not exercise a megabyte-scale body")
	}
	output, err := compile(t, requestSource).NewTurn().ApplyRequest(context.Background(), messagetransform.RequestMessage{
		Method: "POST", Path: "/v1/responses", Body: body, Headers: http.Header{"Version": {"9.8.7"}},
	})
	if err != nil || !bytes.Equal(output.Body, body) || output.Headers.Get("Version") != "0.0.0" {
		t.Fatalf("large Unicode body changed or failed: %v", err)
	}
}

func TestAcceptanceMetadataAmbiguousHeaderNamesFailClosed(t *testing.T) {
	for _, name := range []string{"Version", "X-Install-Id", "User-Agent"} {
		t.Run(name, func(t *testing.T) {
			headers := http.Header{name: {"first-value"}, strings.ToLower(name): {"second-value"}}
			_, err := compile(t, requestSource).NewTurn().ApplyRequest(context.Background(), messagetransform.RequestMessage{
				Method: "POST", Path: "/v1/responses", Headers: headers, Body: []byte("{}"),
			})
			if err == nil {
				t.Fatal("ambiguous case-folded input headers were accepted")
			}
		})
	}
}
