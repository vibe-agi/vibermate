package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"strings"
	"testing"
)

func TestPlaceholderKeepsLengthAndIsDeterministic(t *testing.T) {
	t.Parallel()
	for _, original := range []string{"", "a", "short", strings.Repeat("prose ", 100), "café 中文 text that is long enough"} {
		placeholder := Placeholder(original)
		if len(placeholder) != len(original) {
			t.Fatalf("Placeholder(%q) has %d bytes, want %d", original, len(placeholder), len(original))
		}
		if placeholder != Placeholder(original) {
			t.Fatalf("Placeholder(%q) is not deterministic", original)
		}
		for _, character := range placeholder {
			if character > 0x7e {
				t.Fatalf("Placeholder(%q) is not ASCII: %q", original, placeholder)
			}
		}
	}
	if Placeholder(strings.Repeat("a", 200)) == Placeholder(strings.Repeat("b", 200)) {
		t.Fatal("different prose gave the same placeholder")
	}
}

func TestSkeletonReplacesProseAndKeepsStructure(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("You are a helpful agent. ", 20)
	input := `{"model":"claude-opus-5-5","z_last_first":1,"system":[` +
		`{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.329.580; cch=58bc8; cc_prompt_id=eda013f9-6afe-44c7-9070-3a95bf45cb70;"},` +
		`{"type":"text","text":` + quote(long) + `,"cache_control":{"type":"ephemeral"}}],` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"Reply with the fixture text"}]}],` +
		`"tools":[{"name":"Bash","description":` + quote(long) + `,"input_schema":{"type":"object","properties":{"command":{"type":"string"}}}}],` +
		`"max_tokens":128000,"temperature":1.0e0,"a":true,"b":null}`
	output, err := Skeleton([]byte(input), DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(output) {
		t.Fatalf("output is not JSON: %s", output)
	}
	text := string(output)
	if strings.Contains(text, "helpful agent") {
		t.Fatalf("prose survived: %s", text)
	}
	for _, kept := range []string{
		`"model":"claude-opus-5-5","z_last_first":1,"system"`,
		`"text":"x-anthropic-billing-header: cc_version=2.1.329.580; cch=`,
		`"text":"Reply with the fixture text"`,
		`"name":"Bash"`,
		`"input_schema":{"type":"object","properties":{"command":{"type":"string"}}}`,
		`"max_tokens":128000,"temperature":1.0e0,"a":true,"b":null}`,
		`"cache_control":{"type":"ephemeral"}`,
	} {
		if !strings.Contains(text, kept) {
			t.Fatalf("output lost %s: %s", kept, text)
		}
	}
	if strings.Contains(text, "58bc8") || strings.Contains(text, "eda013f9-6afe-44c7-9070-3a95bf45cb70") {
		t.Fatalf("attribution identifiers survived: %s", text)
	}
	var decoded struct {
		System []struct {
			Text string `json:"text"`
		} `json:"system"`
		Tools []struct {
			Description string `json:"description"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(output, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.System[1].Text) != len(long) || len(decoded.Tools[0].Description) != len(long) {
		t.Fatalf("placeholders changed length: %d %d want %d", len(decoded.System[1].Text), len(decoded.Tools[0].Description), len(long))
	}
	billing := "x-anthropic-billing-header: cc_version=2.1.329.580; cch=58bc8; cc_prompt_id=eda013f9-6afe-44c7-9070-3a95bf45cb70;"
	if len(decoded.System[0].Text) != len(billing) {
		t.Fatalf("billing line changed length: %q", decoded.System[0].Text)
	}
	again, err := Skeleton([]byte(input), DefaultOptions())
	if err != nil || !bytes.Equal(again, output) {
		t.Fatalf("Skeleton is not deterministic: %v", err)
	}
}

func TestSkeletonReplacesIdentifiersConsistently(t *testing.T) {
	t.Parallel()
	session := "72b6d2dc-7e45-4e8f-9b85-b761f5e05790"
	device := "756568c384d1557a3ef43750dbe47677ecaefe7706cf126d5212cbd39de5dca7"
	input := `{"metadata":{"user_id":"{\"device_id\":\"` + device + `\",\"account_uuid\":\"\",\"session_id\":\"` + session + `\"}"},` +
		`"prompt_cache_key":"` + session + `","id":"msg_01a1fa13-5067-7df0-b6a3-b87f1cec5ce4",` +
		`"file_path":"/tmp/TestWireDumpClaudeToolRoundTrip4241879820/002/wire-fixture.png","cwd":"/home/someone/project","owner":"someone-else",` +
		`"slugs":"/tmp/x/.claude/projects/-tmp-TestWireDump1234-002/a and -home-someone-project"}`
	options := DefaultOptions()
	options.Redact = []string{"someone-else"}
	output, err := Skeleton([]byte(input), options)
	if err != nil {
		t.Fatal(err)
	}
	text := string(output)
	for _, secret := range []string{session, device, "01a1fa13-5067-7df0-b6a3-b87f1cec5ce4", "TestWireDump", "someone"} {
		if strings.Contains(text, secret) {
			t.Fatalf("identifier %q survived: %s", secret, text)
		}
	}
	var decoded struct {
		Metadata struct {
			UserID string `json:"user_id"`
		} `json:"metadata"`
		PromptCacheKey string `json:"prompt_cache_key"`
		ID             string `json:"id"`
		FilePath       string `json:"file_path"`
		Cwd            string `json:"cwd"`
		Owner          string `json:"owner"`
		Slugs          string `json:"slugs"`
	}
	if err := json.Unmarshal(output, &decoded); err != nil {
		t.Fatal(err)
	}
	var user struct {
		DeviceID  string `json:"device_id"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(decoded.Metadata.UserID), &user); err != nil {
		t.Fatalf("nested JSON broke: %v", err)
	}
	if user.SessionID != decoded.PromptCacheKey || len(user.SessionID) != len(session) || user.SessionID[14] != '4' {
		t.Fatalf("session identifier was not replaced consistently: %q %q", user.SessionID, decoded.PromptCacheKey)
	}
	if len(user.DeviceID) != len(device) {
		t.Fatalf("device identifier changed length: %q", user.DeviceID)
	}
	if !strings.HasPrefix(decoded.ID, "msg_") || len(decoded.ID) != len("msg_01a1fa13-5067-7df0-b6a3-b87f1cec5ce4") || decoded.ID[4+14] != '7' {
		t.Fatalf("prefixed identifier = %q", decoded.ID)
	}
	if decoded.FilePath != "/tmp/wire-skeleton/002/wire-fixture.png" || decoded.Cwd != "/home/user/project" {
		t.Fatalf("paths = %q %q", decoded.FilePath, decoded.Cwd)
	}
	if decoded.Owner != "xxxxxxxxxxxx" {
		t.Fatalf("redaction = %q", decoded.Owner)
	}
	if decoded.Slugs != "/tmp/wire-skeleton/.claude/projects/-tmp-wire-skeleton/a and -home-user-project" {
		t.Fatalf("project slugs = %q", decoded.Slugs)
	}
}

func TestSkeletonReplacesMediaWithTinyValidFiles(t *testing.T) {
	t.Parallel()
	large := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x89, 0x50, 0x4e, 0x47}, 4096))
	input := `{"content":[` +
		`{"type":"image","source":{"data":"` + large + `","media_type":"image/png","type":"base64"}},` +
		`{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"` + large + `"}},` +
		`{"type":"image","source":{"type":"base64","media_type":"image/gif","data":"` + large + `"}},` +
		`{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"` + large + `"}},` +
		`{"type":"input_image","image_url":"data:image/png;base64,` + large + `"}]}`
	output, err := Skeleton([]byte(input), DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(output, []byte(large)) {
		t.Fatal("original media survived")
	}
	var decoded struct {
		Content []struct {
			Source struct {
				Type      string `json:"type"`
				MediaType string `json:"media_type"`
				Data      string `json:"data"`
			} `json:"source"`
			ImageURL string `json:"image_url"`
		} `json:"content"`
	}
	if err := json.Unmarshal(output, &decoded); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(output), `{"content":[{"type":"image","source":{"data":"`) {
		t.Fatalf("member order changed: %.80s", output)
	}
	for index, block := range decoded.Content[:3] {
		raw, err := base64.StdEncoding.DecodeString(block.Source.Data)
		if err != nil {
			t.Fatalf("block %d: %v", index, err)
		}
		config, format, err := image.DecodeConfig(bytes.NewReader(raw))
		if err != nil || config.Width != 1 || config.Height != 1 || "image/"+format != block.Source.MediaType {
			t.Fatalf("block %d: %v %s %+v", index, err, format, config)
		}
	}
	pdf, err := base64.StdEncoding.DecodeString(decoded.Content[3].Source.Data)
	if err != nil || !bytes.HasPrefix(pdf, []byte("%PDF-1.4\n")) || !bytes.HasSuffix(pdf, []byte("%%EOF\n")) || !bytes.Contains(pdf, []byte("/Type /Page ")) {
		t.Fatalf("PDF replacement = %q (%v)", pdf, err)
	}
	encoded, ok := strings.CutPrefix(decoded.Content[4].ImageURL, "data:image/png;base64,")
	if !ok {
		t.Fatalf("data URL = %q", decoded.Content[4].ImageURL)
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if _, format, err := image.DecodeConfig(bytes.NewReader(raw)); err != nil || format != "png" {
		t.Fatalf("data URL image: %v %s", err, format)
	}
}

func TestMinimalPDFCrossReferenceIsExact(t *testing.T) {
	t.Parallel()
	document := minimalPDF()
	for object := 1; object <= 3; object++ {
		marker := []byte{byte('0' + object), ' ', '0', ' ', 'o', 'b', 'j'}
		offset := strings.Index(document, string(marker))
		entry := strings.Repeat("0", 10-len(itoa(offset))) + itoa(offset) + " 00000 n "
		if !strings.Contains(document, entry) {
			t.Fatalf("object %d at %d has no exact xref entry", object, offset)
		}
	}
	start := strings.Index(document, "xref\n")
	if !strings.Contains(document, "startxref\n"+itoa(start)+"\n") {
		t.Fatal("startxref does not point at the table")
	}
}

func TestSkeletonEventStreamRewritesOnlyData(t *testing.T) {
	t.Parallel()
	input := "event: content_block_delta\n" +
		`data: {"delta":{"partial_json":"{\"file_path\":\"/tmp/TestWire1234/002/x.png\"}","type":"input_json_delta"},"index":4}` + "\n\n" +
		"data: [DONE]\n\n"
	output, err := SkeletonEventStream([]byte(input), DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	want := "event: content_block_delta\n" +
		`data: {"delta":{"partial_json":"{\"file_path\":\"/tmp/wire-skeleton/002/x.png\"}","type":"input_json_delta"},"index":4}` + "\n\n" +
		"data: [DONE]\n\n"
	if string(output) != want {
		t.Fatalf("event stream =\n%s\nwant\n%s", output, want)
	}
}

func TestSkeletonRejectsTrailingData(t *testing.T) {
	t.Parallel()
	if _, err := Skeleton([]byte(`{} {}`), DefaultOptions()); err == nil {
		t.Fatal("trailing value accepted")
	}
}

func quote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func itoa(value int) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
