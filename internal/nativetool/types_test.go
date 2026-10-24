package nativetool

import (
	"crypto/sha256"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/egressprofile/egressprofiletest"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

func bindingFixture(t *testing.T, mutations ...func(*environment.Environment)) BindingInput {
	input := authoredBindingFixture(t, mutations...)
	profile, explicit := input.Plan.Route().EgressProfile.ExplicitProfile()
	if !explicit {
		t.Fatal("fixture requires explicit profile")
	}
	resolved, err := egressprofile.RestoreResolved(profile, egressprofile.SelectionFlow, 0)
	if err != nil {
		t.Fatal(err)
	}
	input.Plan, err = input.Plan.WithResolvedEgress(resolved)
	if err != nil {
		t.Fatal(err)
	}
	return input
}

func authoredBindingFixture(t *testing.T, mutations ...func(*environment.Environment)) BindingInput {
	t.Helper()
	origin, _ := originidentity.ParseProviderOrigin("https://chatgpt.com")
	operations, _ := operationcatalog.BuiltIn()
	pairID, _ := protocolspec.NewCodecPairID("native.test")
	catalog, err := protocolspec.NewCatalog(operations.Definitions(), []protocolspec.CodecPairDefinition{{ID: pairID, Revision: 1, ClientDialect: protocolspec.DialectOpenAIResponses, ProviderDialect: protocolspec.DialectOpenAIResponses, ClientOperationIDs: operations.SemanticOperationIDs(protocolspec.DialectOpenAIResponses), RequiredCapabilities: []protocolspec.ProviderCapability{protocolspec.ProviderCapabilityMessages, protocolspec.ProviderCapabilityStreaming, protocolspec.ProviderCapabilityToolCalls}}})
	if err != nil {
		t.Fatal(err)
	}
	wires, _ := wireprofile.BuiltInCatalog()
	compiler, _ := environment.NewCompiler(nil, nil, catalog, wires)
	aggregate := environment.Environment{ID: "native", Name: "Native", State: environment.StateActive, Revision: 1,
		ContentRecording: environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff},
		ClientToolAccess: &environment.ClientToolAccessPolicy{Enabled: true, Route: &environment.ClientToolRoute{ID: "native.mcp", Revision: 1, TargetOrigin: origin, Path: "/backend-api/ps/mcp", CredentialOwner: environment.ClientToolCredentialOriginal, EgressProfile: egressprofiletest.Explicit(t, egressprofile.Direct()), Contract: environment.ClientToolContractCodexLegacy}}}
	for _, mutate := range mutations {
		mutate(&aggregate)
	}
	snapshot, err := compiler.Compile(aggregate)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := snapshot.BeginProviderConnection(aggregate.ClientToolAccess.Route.TargetOrigin)
	if err != nil {
		t.Fatal(err)
	}
	request, err := snapshot.ResolveClientToolRequest(connection, environment.RequestFacts{Target: protocolspec.RequestTarget{Method: "POST", Path: "/backend-api/ps/mcp", Transport: protocolspec.ClientOperationTransportHTTP}})
	if err != nil {
		t.Fatal(err)
	}
	plan, ok := request.ClientToolPlan()
	if !ok {
		t.Fatal("missing native plan")
	}
	key, _ := protocolcore.NewCallKey("mcp", "1")
	args, _ := protocolcore.NewJSONObject([]byte(`{"path":"a"}`), 1024)
	catalogDigest := Digest(sha256.Sum256([]byte("catalog")))
	return BindingInput{Kind: RequestToolsCall, Plan: plan, Capture: captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "capture"}, AssignmentRevision: 1, OwnerEpoch: 1,
		SessionID: ID(strings.Repeat("a", 32)), SessionRevision: 1, CatalogGeneration: 1, CatalogDigest: &catalogDigest,
		ObservedCallID: []byte("observed-only"), ExactRPC: []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"}},"_meta":{"trace":"first"}}`),
		Intents: []protocolcore.ToolIntent{{ResponseID: "rpc-1", Ordinal: 0, Call: protocolcore.ToolCall{Key: key, Name: "read_file", Arguments: args}}}}
}

func TestNativeBindingReplayScopeSurvivesEnvironmentAndRouteReplacement(t *testing.T) {
	original, err := NewBinding(bindingFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	key, ok := original.ReplayKey()
	if !ok {
		t.Fatal("missing observed correlation")
	}
	for _, test := range []struct {
		name   string
		mutate func(*environment.Environment)
	}{
		{"environment", func(e *environment.Environment) { e.ID = "replacement" }},
		{"route", func(e *environment.Environment) { e.ClientToolAccess.Route.ID = "replacement.mcp" }},
		{"revisions", func(e *environment.Environment) { e.Revision++; e.ClientToolAccess.Route.Revision++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := bindingFixture(t, test.mutate)
			input.OwnerEpoch++
			input.AssignmentRevision++
			input.SessionRevision++
			input.SessionID = ID(strings.Repeat("b", 32))
			replacement, err := NewBinding(input)
			if err != nil {
				t.Fatal(err)
			}
			replacementKey, _ := replacement.ReplayKey()
			if replacementKey != key {
				t.Fatal("same Capture and actual target lost observed-call replay correlation")
			}
			if replacement.Digest() == original.Digest() {
				t.Fatal("replacement authority failed to change binding digest")
			}
		})
	}
	differentTarget := bindingFixture(t, func(e *environment.Environment) {
		e.ClientToolAccess.Route.TargetOrigin, _ = originidentity.ParseProviderOrigin("https://other.example")
	})
	for _, input := range []BindingInput{differentTarget, func() BindingInput { i := bindingFixture(t); i.Capture.ID = "different"; return i }(), func() BindingInput {
		i := bindingFixture(t)
		i.Capture.Kind = captureidentity.KindManualCapture
		return i
	}(), func() BindingInput {
		i := bindingFixture(t)
		i.ObservedCallID = []byte("another observed call")
		return i
	}()} {
		binding, err := NewBinding(input)
		if err != nil {
			t.Fatal(err)
		}
		otherKey, _ := binding.ReplayKey()
		if otherKey == key {
			t.Fatal("distinct Capture/target/observed identity shared replay correlation")
		}
	}
}

func TestNativeBindingRejectsModelAndManagedOwner(t *testing.T) {
	input := bindingFixture(t)
	input.Plan = environment.ClientToolPlan{}
	if _, err := NewBinding(input); err == nil {
		t.Fatal("zero/model plan admitted")
	}
	for _, mutate := range []func(*BindingInput){
		func(i *BindingInput) { i.Capture.ID = "" }, func(i *BindingInput) { i.OwnerEpoch = 0 }, func(i *BindingInput) { i.AssignmentRevision = 0 },
		func(i *BindingInput) { i.SessionID = "UPPER" }, func(i *BindingInput) { i.SessionRevision = 0 }, func(i *BindingInput) { i.CatalogGeneration = 0 },
		func(i *BindingInput) { i.CatalogDigest = nil }, func(i *BindingInput) { i.Kind = RequestUnclassified }, func(i *BindingInput) { i.Intents = nil },
		func(i *BindingInput) { i.Intents[0].Call.Name = "different" }, func(i *BindingInput) { i.ExactRPC = []byte(`{"jsonrpc":"2.0","method":"ping"}`) },
		func(i *BindingInput) { i.ExactRPC = []byte(`{"jsonrpc":"2.0","id":1,"id":2,"method":"tools/call"}`) }, func(i *BindingInput) { i.ExactRPC = make([]byte, (1<<20)+1) },
		func(i *BindingInput) { i.ObservedCallID = make([]byte, 513) }, func(i *BindingInput) { i.OwnerEpoch = 1 << 63 },
		func(i *BindingInput) { i.ParentOperationID = new(ID) },
		func(i *BindingInput) {
			i.ExactRPC = []byte(`[{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"}}}]`)
		},
		func(i *BindingInput) { i.ExactRPC = append(i.ExactRPC, []byte(` {}`)...) },
		func(i *BindingInput) {
			i.ExactRPC = []byte(strings.Replace(string(i.ExactRPC), `"path":"a"`, `"path":"a","path":"b"`, 1))
		},
		func(i *BindingInput) { i.Intents = append(i.Intents, i.Intents[0]) },
		func(i *BindingInput) {
			i.Intents[0].Call.Arguments, _ = protocolcore.NewJSONObject([]byte(`{"path":"b"}`), 1024)
		},
		func(i *BindingInput) {
			i.ExactRPC = []byte(strings.Replace(string(i.ExactRPC), `"arguments":{"path":"a"}`, `"arguments":{"path":"b"}`, 1))
		},
	} {
		input := bindingFixture(t)
		mutate(&input)
		if _, err := NewBinding(input); err == nil {
			t.Fatal("invalid binding admitted")
		}
	}
	for _, id := range []string{"", strings.Repeat("a", 31), strings.Repeat("A", 32), strings.Repeat("g", 32)} {
		if _, err := NewID(id); err == nil {
			t.Fatal("invalid ID accepted")
		}
	}
	if _, err := NewOperationID(strings.Repeat("f", 32)); err != nil {
		t.Fatal(err)
	}
}

func TestNativeBindingFreezesDigestsAndRestrictsReplayCorrelation(t *testing.T) {
	input := bindingFixture(t)
	binding, err := NewBinding(input)
	if err != nil {
		t.Fatal(err)
	}
	original := binding.Digest()
	replay, ok := binding.ReplayKey()
	if !ok {
		t.Fatal("missing observed correlation")
	}
	input.ExactRPC[0] = 'x'
	input.ObservedCallID[0] = 'x'
	*input.CatalogDigest = Digest{}
	input.Intents[0].Call.Name = "changed"
	if binding.Digest() != original || !binding.MatchesIntents(bindingFixture(t).Intents) {
		t.Fatal("input mutation changed binding")
	}
	exposed := binding.SafeCallRefs()
	exposed[0] = "changed"
	if binding.SafeCallRefs()[0] == "changed" {
		t.Fatal("slice getter escaped")
	}
	changed := bindingFixture(t)
	changed.SessionID = ID(strings.Repeat("b", 32))
	changed.SessionRevision++
	changed.ExactRPC = []byte(strings.Replace(string(changed.ExactRPC), `"id":1`, `"id":2`, 1))
	next, err := NewBinding(changed)
	if err != nil {
		t.Fatal(err)
	}
	nextReplay, _ := next.ReplayKey()
	if nextReplay != replay || next.Digest() == original {
		t.Fatal("RPC/session IDs changed observed correlation or failed binding freeze")
	}
	absent := bindingFixture(t)
	absent.ObservedCallID = nil
	without, err := NewBinding(absent)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := without.ReplayKey(); ok {
		t.Fatal("guessed stable replay ID")
	}
	for _, mutate := range []func(*BindingInput){func(i *BindingInput) { i.OwnerEpoch++ }, func(i *BindingInput) { i.AssignmentRevision++ }, func(i *BindingInput) { i.ExactRPC = []byte(strings.Replace(string(i.ExactRPC), "first", "second", 1)) }, func(i *BindingInput) { i.Capture.ID = "other" }} {
		i := bindingFixture(t)
		mutate(&i)
		b, err := NewBinding(i)
		if err != nil || b.Digest() == original {
			t.Fatal("authority/RPC change did not change binding")
		}
	}
	forged := bindingFixture(t)
	schema, _ := protocolcore.NewJSONObject([]byte(`{"type":"object"}`), 1024)
	forged.PublicToolDefinition = &protocolcore.ToolDefinition{Name: "read_file", InputSchema: schema}
	b, err := NewBinding(forged)
	if err != nil || b.ToolName() != "" {
		t.Fatal("unproved catalog definition granted public name")
	}
	forged.PublicToolDefinition.Name = "another"
	if _, err := NewBinding(forged); err == nil {
		t.Fatal("unrelated public definition admitted")
	}
	parentInput := bindingFixture(t)
	parentInput.Kind = RequestServerReply
	parentInput.ExactRPC = []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`)
	parentInput.Intents = nil
	parentInput.ObservedCallID = nil
	parent := ID(strings.Repeat("b", 32))
	server := Digest(sha256.Sum256([]byte("server request")))
	parentInput.ParentOperationID = &parent
	parentInput.ServerRequestDigest = &server
	child, err := NewBinding(parentInput)
	if err != nil {
		t.Fatal(err)
	}
	parent = "invalid"
	server = Digest{}
	frozenParent, ok := child.ParentOperationID()
	frozenServer, serverOK := child.ServerRequestDigest()
	if !ok || !serverOK || frozenParent != ID(strings.Repeat("b", 32)) || frozenServer == (Digest{}) {
		t.Fatal("parent pointer mutation escaped")
	}
}

func TestNativeBindingKindSpecificInputs(t *testing.T) {
	for _, test := range []struct {
		kind    RequestKind
		rpc     string
		catalog bool
	}{
		{RequestInitialize, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, false},
		{RequestInitialized, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, false},
		{RequestToolsList, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, false},
		{RequestPing, `{"jsonrpc":"2.0","id":1,"method":"ping"}`, false},
		{RequestCancel, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`, false},
		{RequestGetStream, "", false}, {RequestDeleteSession, "", false},
		{RequestServerReply, `{"jsonrpc":"2.0","id":1,"result":{"accepted":true}}`, true},
	} {
		t.Run(string(test.kind), func(t *testing.T) {
			i := bindingFixture(t)
			i.Kind = test.kind
			i.ExactRPC = []byte(test.rpc)
			i.Intents = nil
			i.ObservedCallID = nil
			if !test.catalog {
				i.CatalogGeneration = 0
				i.CatalogDigest = nil
			}
			if test.kind == RequestServerReply {
				parent := ID(strings.Repeat("b", 32))
				digest := Digest(sha256.Sum256([]byte("observed server request")))
				i.ParentOperationID = &parent
				i.ServerRequestDigest = &digest
			}
			if _, err := NewBinding(i); err != nil {
				t.Fatal(err)
			}
			i.SessionID = ""
			if _, err := NewBinding(i); err == nil {
				t.Fatal("missing session admitted")
			}
		})
	}
}
