package runtimepersistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/exchangecontent"
)

func TestContentManifestPreservesRetainedIdentityLanguage(t *testing.T) {
	ctx := context.Background()
	l := candidateContentLimits()
	r := sourceOnlyRecord(t, "retained\tidentity", sourceText("body"))
	r.Request.Tools = []exchangecontent.ToolDefinition{{Name: "tool\tname"}}
	source, err := exchangecontent.SourceFromRecordWithin(l, r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = source.Measure(ctx); err != nil {
		t.Fatal(err)
	}
	_, encoded, _, err := encodeStoredContent(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = decodeStoredContentManifestWithin(ctx, encoded, &l); err != nil {
		t.Fatalf("manifest narrowed retained identity language: %v", err)
	}
}

func TestContentManifestContainerAdmissionBeforeDecode(t *testing.T) {
	l := candidateContentLimits()
	r := sourceOnlyRecord(t, "manifest-admission", sourceText("body"))
	source, err := exchangecontent.SourceFromRecordWithin(l, r)
	if err != nil {
		t.Fatal(err)
	}
	cost, err := source.Measure(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	l.RetainedBytes, l.StructureBytes = cost.RetainedBytes, cost.StructureBytes
	s := openCandidateContentStore(t, filepath.Join(t.TempDir(), "runtime.db"), &l)
	if err := putCandidateSource(t, s, source); err != nil {
		t.Fatal(err)
	}
	var original []byte
	if err := s.database.QueryRow(`SELECT manifest_json FROM runtime_exchange_contents WHERE exchange_id=?`, r.ExchangeID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	bad := bytes.Replace(original, []byte(`"tools":null`), []byte(`"tools":[`+strings.Repeat(`{},`, 19999)+`{}]`), 1)
	if bytes.Equal(bad, original) {
		t.Fatal("fixture did not replace tools")
	}
	if _, err := s.database.Exec(`UPDATE runtime_exchange_contents SET manifest_json=? WHERE exchange_id=?`, bad, r.ExchangeID); err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got, err := s.exchangeContents.Get(context.Background(), r.ExchangeID, r.RecordedAt)
	runtime.ReadMemStats(&after)
	if !errors.Is(err, exchangecontent.ErrInvalidEvidence) || got.ExchangeID != "" {
		t.Fatalf("invalid manifest: %v", err)
	}
	if n := after.TotalAlloc - before.TotalAlloc; n > 512<<10 {
		t.Fatalf("manifest containers allocated before admission: %d", n)
	}
}

func TestContentManifestExactMetadataBudgetsAndLanguage(t *testing.T) {
	r := sourceOnlyRecord(t, "manifest-boundaries", sourceText("body"))
	r.Request.RequestedModel = strings.Repeat("m", 20000)
	l := candidateContentLimits()
	source, err := exchangecontent.SourceFromRecordWithin(l, r)
	if err != nil {
		t.Fatal(err)
	}
	cost, err := source.Measure(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, encoded, _, err := encodeStoredContent(r)
	if err != nil {
		t.Fatal(err)
	}
	l.RetainedBytes = cost.RetainedBytes - uint64(len("usertextrecordedbody"))
	l.StructureBytes = cost.StructureBytes - uint64(reflect.TypeFor[exchangecontent.Message]().Size()+reflect.TypeFor[exchangecontent.Block]().Size())
	for _, currency := range []string{"payload", "structure"} {
		for _, delta := range []int{0, -1} {
			limits := l
			if currency == "payload" && delta < 0 {
				limits.RetainedBytes--
			}
			if currency == "structure" && delta < 0 {
				limits.StructureBytes--
			}
			_, err := decodeStoredContentManifestWithin(context.Background(), encoded, &limits)
			if (err != nil) != (delta < 0) {
				t.Fatalf("%s at%d: %v", currency, delta, err)
			}
		}
	}
	legacy, err := decodeStoredContentManifest(encoded)
	if err != nil {
		t.Fatal(err)
	}
	again, err := json.Marshal(legacy)
	if err != nil || !bytes.Equal(again, encoded) {
		t.Fatal("legacy manifest bytes changed")
	}
	for _, bad := range [][]byte{append(append([]byte{}, encoded...), ' '), bytes.Replace(encoded, []byte(`"exchangeId":`), []byte(`"unknown":`), 1), bytes.Replace(encoded, []byte(`"mode":"full"`), []byte(`"mode":"full","mode":"full"`), 1), bytes.Replace(encoded, []byte(`"stream":false`), []byte(`"stream":0`), 1), bytes.Replace(encoded, []byte(`"maxOutputTokens":16`), []byte(`"maxOutputTokens":-1`), 1)} {
		if bytes.Equal(bad, encoded) {
			t.Fatal("unchanged malformed fixture")
		}
		if _, err := decodeStoredContentManifestWithin(context.Background(), bad, &l); !errors.Is(err, exchangecontent.ErrInvalidEvidence) {
			t.Fatalf("malformed manifest accepted: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := decodeStoredContentManifestWithin(ctx, encoded, &l); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation changed: %v", err)
	}
}

func TestContentManifestCompletionAdmitsBeforeDecode(t *testing.T) {
	l := candidateContentLimits()
	r := sourceOnlyRecord(t, "manifest-completion", sourceText("body"))
	s := openCandidateContentStore(t, filepath.Join(t.TempDir(), "runtime.db"), &l)
	if err := s.exchangeContents.Put(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	var encoded []byte
	if err := s.database.QueryRow(`SELECT manifest_json FROM runtime_exchange_contents`).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	bad := bytes.Replace(encoded, []byte(`"tools":null`), []byte(`"tools":[`+strings.Repeat(`{},`, 19999)+`{}]`), 1)
	if bytes.Equal(bad, encoded) {
		t.Fatal("unchanged fixture")
	}
	if _, err := s.database.Exec(`UPDATE runtime_exchange_contents SET manifest_json=?`, bad); err != nil {
		t.Fatal(err)
	}
	r.Response = contentRecordFixture(t, "template", r.RecordedAt).Response
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	err := s.exchangeContents.Put(context.Background(), r)
	runtime.ReadMemStats(&after)
	if !errors.Is(err, exchangecontent.ErrInvalidEvidence) || after.TotalAlloc-before.TotalAlloc > 512<<10 {
		t.Fatalf("completion manifest allocation=%d err=%v", after.TotalAlloc-before.TotalAlloc, err)
	}
	if countRows(t, s, "runtime_exchange_contents") != 1 || countRows(t, s, "runtime_exchange_content_messages") != 1 {
		t.Fatal("failed completion published data")
	}
}
