package runtimepersistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/openairesponses"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func argumentObjectDepth(depth int) string {
	return `{"a":` + strings.Repeat("[", depth-1) + "0" + strings.Repeat("]", depth-1) + "}"
}

func TestContentSourceResponseIndependentDepth(t *testing.T) {
	ctx := context.Background()
	l := candidateContentLimits()
	options := openairesponses.DefaultOptions()
	options.Resources = &l.Semantic
	codec, err := openairesponses.New(options)
	if err != nil {
		t.Fatal(err)
	}
	request := protocolcore.Request{RequestedModel: "model", EffectiveModel: "model", Messages: []protocolcore.Message{{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{{Kind: protocolcore.BlockText, Text: "question"}}}}}
	for _, depth := range []int{1, 9995, 9996, 9997, 9998, 10000, 10001} {
		t.Run(fmt.Sprint(depth), func(t *testing.T) {
			raw := argumentObjectDepth(depth)
			wire, err := json.Marshal(map[string]any{"id": "resp-depth", "created_at": 1, "status": "completed", "model": "model", "output": []any{map[string]any{"type": "function_call", "id": "fc-depth", "call_id": "call-depth", "name": "read", "arguments": raw, "status": "completed"}}, "usage": map[string]any{}})
			if err != nil {
				t.Fatal(err)
			}
			response, _, err := codec.DecodeProviderResponse(request, wire)
			if depth == 10001 {
				if err == nil {
					t.Fatal("codec accepted raw10001")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			base := sourceOnlyRecord(t, "response-depth", sourceText("question"))
			source, err := exchangecontent.NewSourceWithin(l, base.ExchangeID, base.Frozen, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingFull, RetentionDays: 1}, base.RecordedAt, request, &response)
			if err != nil {
				t.Fatalf("Full Source raw%d: %v", depth, err)
			}
			path := filepath.Join(t.TempDir(), "runtime.db")
			store := openCandidateContentStore(t, path, &l)
			if err := putCandidateSource(t, store, source); err != nil {
				t.Fatal(err)
			}
			shutdownTestStore(t, store)
			store = openCandidateContentStore(t, path, &l)
			got, err := store.exchangeContents.Get(ctx, base.ExchangeID, base.RecordedAt)
			if err != nil || !bytes.Equal(got.Response.Blocks[0].Arguments, []byte(raw)) {
				t.Fatalf("complete response: %v", err)
			}
			projection, err := store.exchangeContents.GetProjection(ctx, base.ExchangeID, base.RecordedAt, exchangecontent.RequestViewFull)
			if err != nil || !bytes.Equal(projection.Response.Blocks[0].Arguments, []byte(raw)) {
				t.Fatalf("full projection: %v", err)
			}
			for _, mode := range []environment.ContentRecordingMode{environment.ContentRecordingMetadataOnly, environment.ContentRecordingOff} {
				omitted, err := exchangecontent.NewSourceWithin(l, "mode-depth", base.Frozen, environment.ContentRecordingPolicy{Mode: mode, RetentionDays: 1}, base.RecordedAt, request, &response)
				if mode == environment.ContentRecordingOff {
					if err == nil || omitted != nil {
						t.Fatal("Off retained content")
					}
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := omitted.Walk(ctx, func(_ exchangecontent.Part, _ int, m exchangecontent.MessageSource) error {
					return m.WalkBlocks(ctx, func(b exchangecontent.Block) error {
						if len(b.Arguments) != 0 || b.Text != "" {
							t.Fatal("metadata disclosed body")
						}
						return nil
					})
				}); err != nil {
					t.Fatal(err)
				}
			}
			history := request
			history.Messages = append(history.Messages, protocolcore.Message{Role: protocolcore.RoleAssistant, Blocks: response.Blocks})
			replay, err := exchangecontent.NewSourceWithin(l, "history-depth", base.Frozen, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingFull, RetentionDays: 1}, base.RecordedAt, history, nil)
			if err != nil {
				t.Fatalf("response replay: %v", err)
			}
			if err := putCandidateSource(t, store, replay); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestContentSourceSmallDeepInlineDeferred(t *testing.T) {
	ctx := context.Background()
	l := candidateContentLimits()
	store := openCandidateContentStore(t, filepath.Join(t.TempDir(), "runtime.db"), &l)
	raw := argumentObjectDepth(10000)
	b := exchangecontent.Block{Kind: "tool_call", Availability: exchangecontent.AvailabilityRecorded, CallID: "c", ToolName: "f", Arguments: json.RawMessage(raw)}
	r := sourceOnlyRecord(t, "small-deep", b)
	source, err := exchangecontent.SourceFromRecordWithin(l, r)
	if err != nil {
		t.Fatal(err)
	}
	if err := putCandidateSource(t, store, source); err != nil {
		t.Fatal(err)
	}
	ref, err := loadStoredContentReference(ctx, store.reads, r.ExchangeID, r.RecordedAt)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"request", "message"} {
		c := pageCursor(ref, "request", kind, 1)
		page, err := store.exchangeContents.GetContentPage(ctx, r.ExchangeID, r.RecordedAt, encodeContentCursor(c))
		if err != nil {
			t.Fatal(err)
		}
		blocks := page.Blocks
		if kind == "request" {
			blocks = page.Messages[0].Blocks
		}
		if len(blocks) != 1 || blocks[0].Deferred == nil || len(blocks[0].Arguments) != 0 {
			t.Fatalf("%s materialized small-deep arguments", kind)
		}
		detail, err := store.exchangeContents.GetContentPage(ctx, r.ExchangeID, r.RecordedAt, blocks[0].Deferred.Cursor)
		if err != nil {
			t.Fatal(err)
		}
		if detail.Kind != "arguments" || detail.Text != raw || detail.BlockMetadata == nil || detail.CanonicalCursor == "" {
			t.Fatal("deep detail incomplete")
		}
		var exact []byte
		for cursor := detail.CanonicalCursor; cursor != ""; {
			p, err := store.exchangeContents.GetContentPage(ctx, r.ExchangeID, r.RecordedAt, cursor)
			if err != nil {
				t.Fatal(err)
			}
			exact = append(exact, p.Data...)
			cursor = p.NextCursor
		}
		var want bytes.Buffer
		if err := exchangecontent.WriteCanonicalBlock(&want, b); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(exact, want.Bytes()) {
			t.Fatal("deep exact reconstruction changed bytes")
		}
	}
	initial, err := store.exchangeContents.GetPagedProjection(ctx, r.ExchangeID, r.RecordedAt, exchangecontent.RequestViewFull)
	if err != nil || initial.Request.Messages[0].Blocks[0].Deferred == nil {
		t.Fatalf("initial deep materialization: %v", err)
	}
}

func TestStoredInlineDecisionBeforeAllocation(t *testing.T) {
	for _, raw := range []string{argumentObjectDepth(255), argumentObjectDepth(10000), `{"n":1e999}`, `[0]`, `null`} {
		b := exchangecontent.Block{Kind: "tool_call", Availability: exchangecontent.AvailabilityRecorded, CallID: "c", ToolName: "f", Arguments: json.RawMessage(raw)}
		var wire bytes.Buffer
		if err := exchangecontent.WriteCanonicalBlock(&wire, b); err != nil {
			t.Fatal(err)
		}
		d, open := parserFixture(t, wire.Bytes())
		opens := 0
		counted := func(ctx context.Context) (storedDecodeInput, error) { opens++; return open(ctx) }
		d.reserve = func(c storedDecodeCost) error {
			if c.RetainedBytes > 64 {
				return exchangecontent.ErrInvalidEvidence
			}
			return nil
		}
		result, err := d.decode(context.Background(), counted, environment.ContentRecordingFull, storedDecodeRequest{inline: true, inlineBudget: exchangecontent.PageContentBytes, inlineEnvelope: 3})
		if err != nil || !result.deferred || len(result.arguments) != 0 || opens != 2 {
			t.Fatalf("pre-allocation deferral len%d opens%d: %v", len(raw), opens, err)
		}
	}
}

func TestDeepInitialAndMessageFiniteLedger(t *testing.T) {
	ctx := context.Background()
	l := candidateContentLimits()
	s := openCandidateContentStore(t, filepath.Join(t.TempDir(), "runtime.db"), &l)
	b := exchangecontent.Block{Kind: "tool_call", Availability: exchangecontent.AvailabilityRecorded, CallID: "c", ToolName: "f", Arguments: json.RawMessage(argumentObjectDepth(10000))}
	r := sourceOnlyRecord(t, "ledger-depth", b)
	src, err := exchangecontent.SourceFromRecordWithin(l, r)
	if err != nil {
		t.Fatal(err)
	}
	if err := putCandidateSource(t, s, src); err != nil {
		t.Fatal(err)
	}
	ref, err := loadStoredContentReference(ctx, s.reads, r.ExchangeID, r.RecordedAt)
	if err != nil {
		t.Fatal(err)
	}
	var digest string
	if err := s.database.QueryRow(`SELECT message_digest FROM runtime_exchange_content_transcripts WHERE digest=?`, ref.requestRoot).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	real := s.exchangeContents.loadPhysical
	loads := 0
	s.exchangeContents.loadPhysical = func(ctx context.Context, d string) (int, string, []byte, error) { loads++; return real(ctx, d) }
	fresh := func() *storedReadLedger {
		return &storedReadLedger{limits: l, bound: 6*exchangecontent.MaxEncodedBytes + storedDecodeFixedWorkspace + (8192+10000)*8 + 12000}
	}
	if _, err := s.exchangeContents.readVerifiedMessage(ctx, digest, r.Mode, fresh(), true); !errors.Is(err, exchangecontent.ErrInvalidEvidence) || loads != 1 {
		t.Fatalf("full must exceed finite materialization budget: loads%d %v", loads, err)
	}
	loads = 0
	initial, _, err := s.exchangeContents.inlinePageMessage(ctx, digest, pageCursor(ref, "request", "message", 1), exchangecontent.PageContentBytes, r.Mode, fresh(), 7)
	if err != nil || initial.Blocks[0].Deferred == nil || loads != 2 {
		t.Fatalf("initial before allocation loads%d: %v", loads, err)
	}
	loads = 0
	selected, err := s.exchangeContents.readSelectedMessage(ctx, digest, r.Mode, fresh(), true, &storedMessageReadRequest{})
	if err != nil || len(selected.Deferred) != 1 || loads != 2 {
		t.Fatalf("message before allocation loads%d: %v", loads, err)
	}
	// Both initial and continuation paths must still authenticate the second pass.
	for _, initial := range []bool{false, true} {
		for _, cancelled := range []bool{false, true} {
			loads = 0
			ctx, cancel := context.WithCancel(context.Background())
			s.exchangeContents.loadPhysical = func(ctx context.Context, d string) (int, string, []byte, error) {
				loads++
				n, codec, data, err := real(ctx, d)
				if loads == 2 {
					if cancelled {
						cancel()
					} else {
						data = append([]byte(nil), data...)
						data[len(data)-1] ^= 1
					}
				}
				return n, codec, data, err
			}
			if initial {
				m, _, err := s.exchangeContents.inlinePageMessage(ctx, digest, pageCursor(ref, "request", "message", 1), exchangecontent.PageContentBytes, r.Mode, fresh(), 7)
				if err == nil || !reflect.DeepEqual(m, exchangecontent.Message{}) {
					t.Fatal("initial exposed changed second pass")
				}
			} else {
				m, err := s.exchangeContents.readSelectedMessage(ctx, digest, r.Mode, fresh(), true, &storedMessageReadRequest{})
				if err == nil || !reflect.DeepEqual(m, storedMessageReadResult{}) {
					t.Fatal("selected exposed changed second pass")
				}
			}
			cancel()
		}
	}
}

func TestInlinePrettyAggregateWindow(t *testing.T) {
	ctx := context.Background()
	l := candidateContentLimits()
	s := openCandidateContentStore(t, filepath.Join(t.TempDir(), "runtime.db"), &l)
	b := exchangecontent.Block{Kind: "tool_call", Availability: exchangecontent.AvailabilityRecorded, CallID: "c", ToolName: "f", Arguments: json.RawMessage(argumentObjectDepth(254))}
	r := sourceOnlyRecord(t, "aggregate", b, b)
	src, err := exchangecontent.SourceFromRecordWithin(l, r)
	if err != nil {
		t.Fatal(err)
	}
	if err := putCandidateSource(t, s, src); err != nil {
		t.Fatal(err)
	}
	ref, err := loadStoredContentReference(ctx, s.reads, r.ExchangeID, r.RecordedAt)
	if err != nil {
		t.Fatal(err)
	}
	cursor := encodeContentCursor(pageCursor(ref, "request", "message", 1))
	for index := 0; index < 2; index++ {
		p, err := s.exchangeContents.GetContentPage(ctx, r.ExchangeID, r.RecordedAt, cursor)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Blocks) != 1 || p.Offset != index || !bytes.Equal(p.Blocks[0].Arguments, b.Arguments) {
			t.Fatalf("aggregate page%d output count%d", index, len(p.Blocks))
		}
		cursor = p.NextCursor
	}
	if cursor != "" {
		t.Fatal("spurious continuation")
	}
	initial, err := s.exchangeContents.GetPagedProjection(ctx, r.ExchangeID, r.RecordedAt, exchangecontent.RequestViewFull)
	if err != nil || initial.Request.Messages[0].Blocks[0].Deferred == nil {
		t.Fatalf("initial aggregate window bypassed: %v", err)
	}
}

func TestAggregatePrettyDeferralHasExactRoute(t *testing.T) {
	ctx := context.Background()
	l := candidateContentLimits()
	s := openCandidateContentStore(t, filepath.Join(t.TempDir(), "runtime.db"), &l)
	b := exchangecontent.Block{Kind: "tool_call", Availability: exchangecontent.AvailabilityRecorded, CallID: "c", ToolName: "f", Arguments: json.RawMessage(argumentObjectDepth(180))}
	r := sourceOnlyRecord(t, "aggregate-exact", b, b)
	src, err := exchangecontent.SourceFromRecordWithin(l, r)
	if err != nil {
		t.Fatal(err)
	}
	if err := putCandidateSource(t, s, src); err != nil {
		t.Fatal(err)
	}
	ref, err := loadStoredContentReference(ctx, s.reads, r.ExchangeID, r.RecordedAt)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.exchangeContents.GetContentPage(ctx, r.ExchangeID, r.RecordedAt, encodeContentCursor(pageCursor(ref, "request", "message", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Blocks) != 2 || p.Blocks[0].Deferred != nil || p.Blocks[1].Deferred == nil {
		t.Fatal("aggregate expansion did not defer second block")
	}
	detail, err := s.exchangeContents.GetContentPage(ctx, r.ExchangeID, r.RecordedAt, p.Blocks[1].Deferred.Cursor)
	if err != nil || detail.Text != string(b.Arguments) || detail.CanonicalCursor == "" {
		t.Fatalf("aggregate pretty deferral missing complete exact route: %v", err)
	}
}

func TestRetainedDeepLocationsAndFallbackRoutes(t *testing.T) {
	for _, location := range []string{"request", "system", "response"} {
		for _, raw := range []string{argumentObjectDepth(10000), strings.Replace(argumentObjectDepth(10000), "0", `"`+strings.Repeat("x", 40000)+`"`, 1), `{"a":1e999}`, `{"a":1e308}`} {
			t.Run(location+fmt.Sprint(len(raw)), func(t *testing.T) {
				ctx := context.Background()
				l := candidateContentLimits()
				s := openCandidateContentStore(t, filepath.Join(t.TempDir(), "runtime.db"), &l)
				b := exchangecontent.Block{Kind: "tool_call", Availability: exchangecontent.AvailabilityRecorded, CallID: "c", ToolName: "f", Arguments: json.RawMessage(raw)}
				r := sourceOnlyRecord(t, "deep-route", sourceText("question"))
				switch location {
				case "request":
					r.Request.Messages[0].Blocks = []exchangecontent.Block{b}
				case "system":
					r.Request.System = []exchangecontent.Block{b}
				case "response":
					r.Response = &exchangecontent.Response{ID: "response", RequestedModel: "m", EffectiveModel: "m", ReportedModel: "m", StopReason: "tool_use", Blocks: []exchangecontent.Block{b}}
				}
				src, err := exchangecontent.SourceFromRecordWithin(l, r)
				if err != nil {
					t.Fatal(err)
				}
				if err := putCandidateSource(t, s, src); err != nil {
					t.Fatal(err)
				}
				p, err := s.exchangeContents.GetPagedProjection(ctx, r.ExchangeID, r.RecordedAt, exchangecontent.RequestViewFull)
				if err != nil {
					t.Fatal(err)
				}
				blocks := p.Request.Messages[0].Blocks
				if location == "system" {
					blocks = p.Request.System
				}
				if location == "response" {
					blocks = p.Response.Blocks
				}
				if len(blocks) != 1 || blocks[0].Deferred == nil {
					t.Fatal("unsafe inline location")
				}
				var ordinary strings.Builder
				var exactCursor string
				for cursor := blocks[0].Deferred.Cursor; cursor != ""; {
					page, err := s.exchangeContents.GetContentPage(ctx, r.ExchangeID, r.RecordedAt, cursor)
					if err != nil {
						t.Fatal(err)
					}
					if page.Kind != "arguments" || page.BlockMetadata == nil || len(page.Text) > exchangecontent.PageBodyBytes || page.CanonicalCursor == "" {
						t.Fatal("incomplete ordinary argument route")
					}
					ordinary.WriteString(page.Text)
					exactCursor = page.CanonicalCursor
					cursor = page.NextCursor
				}
				if ordinary.String() != raw {
					t.Fatal("argument continuation changed bytes")
				}
				var exact []byte
				for exactCursor != "" {
					page, err := s.exchangeContents.GetContentPage(ctx, r.ExchangeID, r.RecordedAt, exactCursor)
					if err != nil {
						t.Fatal(err)
					}
					if len(page.Data) > exchangecontent.PageBodyBytes {
						t.Fatal("oversize exact chunk")
					}
					exact = append(exact, page.Data...)
					exactCursor = page.NextCursor
				}
				var want bytes.Buffer
				if err := exchangecontent.WriteCanonicalBlock(&want, b); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(exact, want.Bytes()) {
					t.Fatal("canonical continuation changed bytes")
				}
			})
		}
	}
}

func TestRetainedLeafRejectsInvalidBeforePublication(t *testing.T) {
	l := candidateContentLimits()
	s := openCandidateContentStore(t, filepath.Join(t.TempDir(), "runtime.db"), &l)
	for _, raw := range []string{argumentObjectDepth(10001), argumentObjectDepth(10000) + " {}", strings.TrimSuffix(argumentObjectDepth(10000), "}"), `{"a":]}`} {
		b := exchangecontent.Block{Kind: "tool_call", Availability: exchangecontent.AvailabilityRecorded, CallID: "c", ToolName: "f", Arguments: json.RawMessage(raw)}
		r := sourceOnlyRecord(t, "invalid", b)
		if src, err := exchangecontent.SourceFromRecordWithin(l, r); err == nil || src != nil {
			t.Fatal("invalid Source published")
		}
		data := []byte(`{"kind":"tool_call","availability":"recorded","originalSize":0,"callId":"c","toolName":"f","arguments":` + raw + `}`)
		d, open := parserFixture(t, data)
		got, err := d.full(context.Background(), open, environment.ContentRecordingFull)
		if err == nil || !reflect.DeepEqual(got, exchangecontent.Block{}) {
			t.Fatal("invalid stored raw exposed")
		}
	}
	var count int
	if err := s.database.QueryRow(`SELECT count(*) FROM runtime_exchange_contents`).Scan(&count); err != nil || count != 0 {
		t.Fatal("invalid evidence published")
	}
}

type prettyFixture struct {
	Name, Raw      string
	Upper, Debit   uint64
	Inline, Finite bool
}

func TestContentPrettyBoundDartFixture(t *testing.T) {
	cases := []struct{ name, raw string }{
		{"ordinary", `{"a":1}`}, {"path", `{"path":"src/main.go","limit":10}`},
		{"unicode", `{"bmp":"é中","supplementary":"😀","escapes":"\ud800\udc00\ud800\udfff\u2028\u2029\u0000\b\f\n\r\t\"\\\/\u003c\u003e\u0026"}`},
		{"empty", `{"a":[],"b":{},"c":[{},[]]}`}, {"dense", `{"a":[0,1,2,true,false,null,{},[]],"a":[3,4]}`},
		{"integers", `{"a":[0,-0,1,-1,9007199254740991,-9007199254740991,9007199254740992,9223372036854775807,-9223372036854775808]}`},
		{"doubles", `{"a":[0.0,-0.0,1.5,1e-7,1e20,1e21,1.2345678901234567e30,1e307,1e-999,0e999]}`},
		{"nonfinite", `{"a":1e999}`}, {"unproved", `{"a":1e308}`}, {"overflowExponent", `{"a":1e999999999999999999999}`},
		{"fit254", argumentObjectDepth(254)}, {"exceed255", argumentObjectDepth(255)},
	}
	var fixtures []prettyFixture
	for _, c := range cases {
		b := exchangecontent.Block{Kind: "tool_call", Availability: exchangecontent.AvailabilityRecorded, CallID: "c", ToolName: "f", Arguments: json.RawMessage(c.raw)}
		var wire bytes.Buffer
		if err := exchangecontent.WriteCanonicalBlock(&wire, b); err != nil {
			t.Fatal(err)
		}
		d, open := parserFixture(t, wire.Bytes())
		facts, err := d.count(context.Background(), open, environment.ContentRecordingFull)
		if err != nil {
			t.Fatal(err)
		}
		if c.name == "ordinary" && facts.PrettyUpper != 15 || c.name == "path" && facts.PrettyUpper != 46 || c.name == "fit254" && facts.PrettyUpper != 130057 || c.name == "exceed255" && facts.PrettyUpper != 131073 {
			t.Fatalf("hand-derived bound %s=%d", c.name, facts.PrettyUpper)
		}
		fixtures = append(fixtures, prettyFixture{c.name, c.raw, facts.PrettyUpper, facts.inlineDebit(), facts.inlineEligible(3), facts.FiniteNumbers})
	}
	encoded, err := json.Marshal(fixtures)
	if err != nil {
		t.Fatal(err)
	}
	fixture := "// Generated by TestContentPrettyBoundDartFixture; do not hand-edit.\nconst contentPrettyBound = r'''\n" + string(encoded) + "\n''';\n"
	if os.Getenv("CONTENT_PRETTY_FIXTURE_EMIT") == "1" {
		fmt.Println("CONTENT_PRETTY_FIXTURE=" + string(encoded))
	}
	actual, err := os.ReadFile("../../ui/flutter_app/test/fixtures/content_pretty_bound.dart")
	if err != nil || string(actual) != fixture {
		t.Fatalf("Go-produced pretty fixture differs: %v", err)
	}
}
