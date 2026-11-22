package protocolpath

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

// fakeClientSide is an Anthropic client side whose renderer marks the class.
type fakeClientSide struct {
	clientCodecFixture
	dialect protocolspec.Dialect
}

func (side fakeClientSide) Dialect() protocolspec.Dialect {
	if side.dialect != "" {
		return side.dialect
	}
	return protocolspec.DialectAnthropicMessages
}

func (fakeClientSide) NewStreamEncoder(protocolcore.Request) (ClientStreamEncoder, error) {
	return recordingEncoder{}, nil
}

func (side fakeClientSide) RenderProviderError(_ protocolcore.Request, providerError protocolcore.ProviderError) protocolcore.NativeProviderError {
	native := protocolcore.NewNativeProviderError(side.Dialect(), []byte(`{"type":"error","class":"`+string(providerError.Class)+`"}`))
	return native.WithClientStatus(http.StatusBadRequest)
}

// renderingClientSide renders every class as one fixed native error, such as
// none at all or one of another dialect.
type renderingClientSide struct {
	fakeClientSide
	native protocolcore.NativeProviderError
}

func (side renderingClientSide) RenderProviderError(protocolcore.Request, protocolcore.ProviderError) protocolcore.NativeProviderError {
	return side.native
}

// fakeProviderSide is a Responses provider side recording the contexts it
// receives.
type fakeProviderSide struct {
	contexts  *[]ProviderContext
	class     protocolcore.ProviderError
	streamErr error
	// finishErr, when set, is returned by FinishDecoded after Feed succeeded.
	finishErr error
}

func (fakeProviderSide) Dialect() protocolspec.Dialect { return protocolspec.DialectOpenAIResponses }

func (side fakeProviderSide) EncodeRequest(request protocolcore.Request) (ProviderRequest, protocolcore.TranslationReport, error) {
	return side.EncodeRequestFor(ProviderContext{}, request)
}

func (side fakeProviderSide) DecodeResponse(request protocolcore.Request, body []byte) (protocolcore.Response, protocolcore.TranslationReport, error) {
	return side.DecodeResponseFor(ProviderContext{}, request, body)
}

func (fakeProviderSide) ValidateTransformedRequest(body []byte) error {
	if string(body) != "encoded:" {
		return errors.New("re-admission refused")
	}
	return nil
}

func (side fakeProviderSide) EncodeRequestFor(providerContext ProviderContext, _ protocolcore.Request) (ProviderRequest, protocolcore.TranslationReport, error) {
	*side.contexts = append(*side.contexts, providerContext)
	request, err := NewProviderRequest(http.MethodPost, "v1/responses", http.Header{"Accept": {"text/event-stream"}}, []byte("encoded:"+providerContext.ReasoningBinding))
	return request, protocolcore.TranslationReport{}, err
}

func (side fakeProviderSide) DecodeResponseFor(providerContext ProviderContext, _ protocolcore.Request, _ []byte) (protocolcore.Response, protocolcore.TranslationReport, error) {
	*side.contexts = append(*side.contexts, providerContext)
	return terminalResponse(protocolcore.StopReasonEndTurn, textBlock(providerContext.ReasoningBinding)), protocolcore.TranslationReport{}, nil
}

func (side fakeProviderSide) NewProviderStream(providerContext ProviderContext, request protocolcore.Request, encoder ClientStreamEncoder) (Stream, error) {
	*side.contexts = append(*side.contexts, providerContext)
	assembler, err := NewAssembler(request, encoder, AssemblerOptions{})
	if err != nil {
		return nil, err
	}
	return &fakeProviderStream{assembler: assembler, err: side.streamErr, finishErr: side.finishErr}, nil
}

func (side fakeProviderSide) NewAggregatingStream(providerContext ProviderContext, request protocolcore.Request) (Stream, error) {
	*side.contexts = append(*side.contexts, providerContext)
	return &fakeProviderStream{err: side.streamErr, finishErr: side.finishErr}, nil
}

func (side fakeProviderSide) ClassifyRejection(protocolcore.Request, int, http.Header, []byte) protocolcore.ProviderError {
	return side.class
}

// fakeProviderStream feeds each fragment as one text delta.
type fakeProviderStream struct {
	assembler *Assembler
	err       error
	finishErr error
}

func (stream *fakeProviderStream) Feed(_ context.Context, fragment []byte) ([]byte, error) {
	if stream.err != nil {
		return nil, stream.err
	}
	if stream.assembler == nil {
		return nil, nil
	}
	start, err := stream.assembler.Start(StreamStart{ResponseID: "resp_1"})
	if err != nil {
		return nil, err
	}
	text, err := stream.assembler.TextDone(string(fragment), string(fragment))
	return append(start, text...), err
}

func (stream *fakeProviderStream) SemanticProgress() uint64 {
	if stream.assembler == nil {
		return 0
	}
	return stream.assembler.SemanticProgress()
}

func (*fakeProviderStream) TerminalReceived() bool { return true }

func (stream *fakeProviderStream) FinishDecoded(context.Context) (PendingTerminal, error) {
	if stream.err != nil {
		return nil, stream.err
	}
	if stream.finishErr != nil {
		return nil, stream.finishErr
	}
	return nil, errors.New("fake stream has no terminal")
}

type admittingProviderSide struct{ fakeProviderSide }

func (admittingProviderSide) AdmitRequest(request protocolcore.Request) *protocolcore.ProviderError {
	if request.RequestedModel == "refuse" {
		return &protocolcore.ProviderError{Class: protocolcore.ProviderErrorImage, Status: http.StatusBadRequest}
	}
	return nil
}

func (admittingProviderSide) CountInputTokens(_ ProviderContext, request protocolcore.Request) (int, error) {
	return len(request.RequestedModel), nil
}

type sourceClientSide struct{ fakeClientSide }

func (sourceClientSide) EncodeSourceResponse(protocolcore.Request, protocolcore.Response, []byte) ([]byte, protocolcore.TranslationReport, error) {
	return nil, protocolcore.TranslationReport{}, nil
}

type sourceProviderSide struct{ fakeProviderSide }

func (sourceProviderSide) EncodeSourceRequest(protocolcore.Request, []byte, http.Header) (ProviderRequest, protocolcore.TranslationReport, error) {
	return ProviderRequest{}, protocolcore.TranslationReport{}, nil
}

func composeFixture(t *testing.T, provider ProviderSide) *Path {
	t.Helper()
	return composeFixtureWith(t, fakeClientSide{}, provider)
}

func composeFixtureWith(t *testing.T, client ClientSide, provider ProviderSide) *Path {
	t.Helper()
	identifier, err := protocolspec.NewCodecPairID("anthropic-messages-to-openai-responses")
	if err != nil {
		t.Fatal(err)
	}
	operation, err := protocolspec.NewClientOperationID("anthropic-messages-create")
	if err != nil {
		t.Fatal(err)
	}
	path, err := Compose(ComposeOptions{
		ID: identifier, Revision: 1, ClientOperationIDs: []protocolspec.ClientOperationID{operation},
		Client: client, Provider: provider,
	})
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func streamingRequest() protocolcore.Request {
	return protocolcore.Request{RequestedModel: "claude-test", EffectiveModel: "gpt-test", Stream: true}
}

func TestComposeRefusesIncompleteAndSameDialectSides(t *testing.T) {
	t.Parallel()
	identifier, _ := protocolspec.NewCodecPairID("pair")
	operation, _ := protocolspec.NewClientOperationID("create")
	base := ComposeOptions{ID: identifier, Revision: 1, ClientOperationIDs: []protocolspec.ClientOperationID{operation}}
	var contexts []ProviderContext
	provider := fakeProviderSide{contexts: &contexts}
	for name, options := range map[string]ComposeOptions{
		"no client":               {ID: base.ID, Revision: 1, ClientOperationIDs: base.ClientOperationIDs, Provider: provider},
		"no provider":             {ID: base.ID, Revision: 1, ClientOperationIDs: base.ClientOperationIDs, Client: fakeClientSide{}},
		"same dialect":            {ID: base.ID, Revision: 1, ClientOperationIDs: base.ClientOperationIDs, Client: fakeClientSide{dialect: protocolspec.DialectOpenAIResponses}, Provider: provider},
		"source response client":  {ID: base.ID, Revision: 1, ClientOperationIDs: base.ClientOperationIDs, Client: sourceClientSide{}, Provider: provider},
		"source request provider": {ID: base.ID, Revision: 1, ClientOperationIDs: base.ClientOperationIDs, Client: fakeClientSide{}, Provider: sourceProviderSide{provider}},
		"no operations":           {ID: base.ID, Revision: 1, Client: fakeClientSide{}, Provider: provider},
		"no revision":             {ID: base.ID, ClientOperationIDs: base.ClientOperationIDs, Client: fakeClientSide{}, Provider: provider},
	} {
		if _, err := Compose(options); err == nil {
			t.Fatalf("Compose accepted %s", name)
		}
	}
}

func TestComposedPathPassesTheProviderContextAtEverySite(t *testing.T) {
	t.Parallel()
	var contexts []ProviderContext
	path := composeFixture(t, fakeProviderSide{contexts: &contexts})
	if !path.IsComposed() || !path.AggregatesUpstreamStream() ||
		path.Client().Dialect() != protocolspec.DialectAnthropicMessages || path.Backend().Dialect() != protocolspec.DialectOpenAIResponses {
		t.Fatal("composed path lost its sides")
	}
	attempt := ProviderContext{Target: TargetOpenAIPlatform, ReasoningBinding: NewReasoningBinding("target.openai.official", "account-1")}
	request := streamingRequest()
	encoded, _, err := path.EncodeProviderRequestFor(attempt, request, []byte("ignored source"), nil)
	if err != nil || string(encoded.Body()) != "encoded:"+attempt.ReasoningBinding || encoded.RelativePath() != "v1/responses" {
		t.Fatalf("EncodeProviderRequestFor() = %q, %v", encoded.Body(), err)
	}
	// The context-free form reaches the provider side with a zero context.
	plain, _, err := path.EncodeProviderRequest(request, nil, nil)
	if err != nil || string(plain.Body()) != "encoded:" {
		t.Fatalf("EncodeProviderRequest() = %q, %v", plain.Body(), err)
	}
	if err := path.ValidateTransformedRequest(plain.Body()); err != nil {
		t.Fatalf("re-admission: %v", err)
	}
	response, _, err := path.DecodeProviderResponseFor(attempt, request, nil)
	if err != nil || response.Blocks[0].Text != attempt.ReasoningBinding {
		t.Fatalf("DecodeProviderResponseFor() = %+v, %v", response, err)
	}
	stream, err := path.NewStreamFor(attempt, request)
	if err != nil {
		t.Fatal(err)
	}
	live, err := stream.Feed(context.Background(), []byte("hi"))
	if err != nil || string(live) != "S(resp_1) T0[ t0:hi ]0 " {
		t.Fatalf("composed stream drove the client encoder: %q, %v", live, err)
	}
	if _, err := path.Streaming().NewStream(request); err != nil {
		t.Fatal(err)
	}
	if _, err := path.NewAggregatingStream(attempt, request); err != nil {
		t.Fatal(err)
	}
	want := []ProviderContext{attempt, {}, attempt, attempt, {}, attempt}
	if len(contexts) != len(want) {
		t.Fatalf("provider saw %d contexts, want %d", len(contexts), len(want))
	}
	for index := range want {
		if contexts[index].Target != want[index].Target || contexts[index].ReasoningBinding != want[index].ReasoningBinding {
			t.Fatalf("context %d = %+v, want %+v", index, contexts[index], want[index])
		}
	}
}

func TestComposedPathTranslatesRejectionsAndStreamFailures(t *testing.T) {
	t.Parallel()
	var contexts []ProviderContext
	class := protocolcore.ProviderError{Class: protocolcore.ProviderErrorReasoningState, Status: http.StatusBadRequest, Code: "invalid_encrypted_content"}
	path := composeFixture(t, fakeProviderSide{contexts: &contexts, class: class})
	request := streamingRequest()
	native, got, ok := path.TranslateProviderRejection(request, http.StatusBadRequest, nil, []byte(`{"error":{}}`))
	if !ok || got != class || native.ClientStatus(protocolspec.DialectAnthropicMessages) != http.StatusBadRequest ||
		!strings.Contains(string(native.ForDialect(protocolspec.DialectAnthropicMessages)), "reasoning_state") {
		t.Fatalf("TranslateProviderRejection() = %v %+v %v", native.ForDialect(protocolspec.DialectAnthropicMessages), got, ok)
	}
	rendered, err := path.RenderProviderError(request, class)
	if err != nil || rendered.ForDialect(protocolspec.DialectAnthropicMessages) == nil {
		t.Fatalf("RenderProviderError() = %v", err)
	}
	if _, err := path.RenderProviderError(request, protocolcore.ProviderError{}); err == nil {
		t.Fatal("an unclassified error was rendered")
	}
	// A provider side that cannot classify leaves today's generic envelope.
	unclassified := composeFixture(t, fakeProviderSide{contexts: &contexts})
	if _, _, ok := unclassified.TranslateProviderRejection(request, http.StatusBadRequest, nil, nil); ok {
		t.Fatal("an unclassified rejection was translated")
	}
	// A stream failure carrying a class leaves Feed with the client's error.
	failure := protocolcore.NewFailure(protocolcore.ReasonProviderResponseFailed, "$", errors.New("response.failed"))
	failure.Provider = &protocolcore.ProviderError{Class: protocolcore.ProviderErrorOverloaded, Code: "server_is_overloaded"}
	failing := composeFixture(t, fakeProviderSide{contexts: &contexts, streamErr: failure})
	stream, err := failing.NewStreamFor(ProviderContext{}, request)
	if err != nil {
		t.Fatal(err)
	}
	_, err = stream.Feed(context.Background(), []byte("x"))
	nativeErr := protocolcore.NativeProviderErrorOf(err)
	if !strings.Contains(string(nativeErr.ForDialect(protocolspec.DialectAnthropicMessages)), "overloaded") {
		t.Fatalf("Feed() did not render the class: %v", err)
	}
	if _, err := stream.FinishDecoded(context.Background()); protocolcore.NativeProviderErrorOf(err).ForDialect(protocolspec.DialectAnthropicMessages) == nil {
		t.Fatalf("FinishDecoded() did not render the class: %v", err)
	}
	// A failure without a class passes through unchanged.
	plain := protocolcore.NewFailure(protocolcore.ReasonMalformedEventStream, "$", errors.New("bad frame"))
	passing := composeFixture(t, fakeProviderSide{contexts: &contexts, streamErr: plain})
	stream, err = passing.NewStreamFor(ProviderContext{}, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Feed(context.Background(), []byte("x")); !errors.Is(err, plain) || protocolcore.NativeProviderErrorOf(err).ForDialect(protocolspec.DialectAnthropicMessages) != nil {
		t.Fatalf("an unclassified failure was rewritten: %v", err)
	}
}

// A client side that renders nothing of its own dialect leaves the caller
// today's generic envelope, for a rejection and for a stream failure alike.
func TestComposedPathNeedsTheClientHalfOfATranslation(t *testing.T) {
	t.Parallel()
	var contexts []ProviderContext
	class := protocolcore.ProviderError{Class: protocolcore.ProviderErrorRateLimited, Status: http.StatusTooManyRequests}
	failure := func() *protocolcore.Failure {
		failure := protocolcore.NewFailure(protocolcore.ReasonProviderResponseFailed, "$", errors.New("response.failed"))
		failure.Provider = &class
		return failure
	}
	for name, native := range map[string]protocolcore.NativeProviderError{
		"no native error":       {},
		"another dialect's one": protocolcore.NewNativeProviderError(protocolspec.DialectOpenAIChat, []byte(`{"error":{"type":"rate_limit"}}`)),
	} {
		client := renderingClientSide{native: native}
		path := composeFixtureWith(t, client, fakeProviderSide{contexts: &contexts, class: class})
		if rendered, got, ok := path.TranslateProviderRejection(streamingRequest(), http.StatusTooManyRequests, nil, nil); ok ||
			got != (protocolcore.ProviderError{}) || rendered.ForDialect(protocolspec.DialectAnthropicMessages) != nil {
			t.Fatalf("%s: TranslateProviderRejection() = %+v, %v", name, got, ok)
		}
		failing := composeFixtureWith(t, client, fakeProviderSide{contexts: &contexts, streamErr: failure()})
		stream, err := failing.NewStreamFor(ProviderContext{}, streamingRequest())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := stream.Feed(context.Background(), []byte("x")); protocolcore.NativeProviderErrorOf(err).ForDialect(protocolspec.DialectOpenAIChat) != nil ||
			protocolcore.NativeProviderErrorOf(err).ForDialect(protocolspec.DialectAnthropicMessages) != nil {
			t.Fatalf("%s: Feed() attached a native error: %v", name, err)
		}
	}
}

// Every stream a composed path hands out renders classified failures: the
// aggregating stream too, and a failure that first appears at FinishDecoded.
// A class that does not validate is never rendered.
func TestComposedStreamsRenderEveryClassifiedFailure(t *testing.T) {
	t.Parallel()
	var contexts []ProviderContext
	classified := func(class protocolcore.ProviderErrorClass) *protocolcore.Failure {
		failure := protocolcore.NewFailure(protocolcore.ReasonProviderResponseFailed, "$", errors.New("response.failed"))
		failure.Provider = &protocolcore.ProviderError{Class: class}
		return failure
	}
	rendered := func(err error) bool {
		return strings.Contains(string(protocolcore.NativeProviderErrorOf(err).ForDialect(protocolspec.DialectAnthropicMessages)), "overloaded")
	}
	request := streamingRequest()

	aggregating := composeFixture(t, fakeProviderSide{contexts: &contexts, streamErr: classified(protocolcore.ProviderErrorOverloaded)})
	unary := request
	unary.Stream = false
	stream, err := aggregating.NewAggregatingStream(ProviderContext{}, unary)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Feed(context.Background(), []byte("x")); !rendered(err) {
		t.Fatalf("aggregating Feed() did not render the class: %v", err)
	}
	if _, err := stream.FinishDecoded(context.Background()); !rendered(err) {
		t.Fatalf("aggregating FinishDecoded() did not render the class: %v", err)
	}

	// Feed succeeds; the classified failure is new at FinishDecoded.
	late := composeFixture(t, fakeProviderSide{contexts: &contexts, finishErr: classified(protocolcore.ProviderErrorOverloaded)})
	stream, err = late.NewStreamFor(ProviderContext{}, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Feed(context.Background(), []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.FinishDecoded(context.Background()); !rendered(err) {
		t.Fatalf("FinishDecoded() did not render a failure that first appeared there: %v", err)
	}

	invalid := composeFixture(t, fakeProviderSide{contexts: &contexts, streamErr: classified("not_a_class")})
	stream, err = invalid.NewStreamFor(ProviderContext{}, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Feed(context.Background(), []byte("x")); err == nil ||
		protocolcore.NativeProviderErrorOf(err).ForDialect(protocolspec.DialectAnthropicMessages) != nil {
		t.Fatalf("an invalid class was rendered: %v", err)
	}
}

func TestComposedPathOptionalCapabilities(t *testing.T) {
	t.Parallel()
	var contexts []ProviderContext
	without := composeFixture(t, fakeProviderSide{contexts: &contexts})
	if without.AdmitProviderRequest(streamingRequest()) != nil {
		t.Fatal("a provider side without admission refused a request")
	}
	if _, err := without.CountInputTokens(ProviderContext{}, streamingRequest()); !errors.Is(err, ErrCapabilityUnsupported) {
		t.Fatalf("CountInputTokens() without a counter = %v", err)
	}
	with := composeFixture(t, admittingProviderSide{fakeProviderSide{contexts: &contexts}})
	refused := streamingRequest()
	refused.RequestedModel = "refuse"
	if admission := with.AdmitProviderRequest(refused); admission == nil || admission.Class != protocolcore.ProviderErrorImage {
		t.Fatalf("AdmitProviderRequest() = %+v", admission)
	}
	if with.AdmitProviderRequest(streamingRequest()) != nil {
		t.Fatal("an admissible request was refused")
	}
	if count, err := with.CountInputTokens(ProviderContext{}, streamingRequest()); err != nil || count != len("claude-test") {
		t.Fatalf("CountInputTokens() = %d, %v", count, err)
	}
}

// Every new Path method keeps today's behaviour on a path built with New.
func TestPathBuiltWithNewFallsBack(t *testing.T) {
	t.Parallel()
	identifier, _ := protocolspec.NewCodecPairID("client-to-provider")
	operation, _ := protocolspec.NewClientOperationID("client-create")
	path, err := New(Options{ID: identifier, Revision: 1, ClientOperationIDs: []protocolspec.ClientOperationID{operation},
		Client: clientCodecFixture{}, Backend: backendCodecFixture{}, Streaming: streamingFixture{}})
	if err != nil {
		t.Fatal(err)
	}
	request := streamingRequest()
	if path.IsComposed() || path.AggregatesUpstreamStream() {
		t.Fatal("a New path reports composition")
	}
	if _, _, err := path.EncodeProviderRequestFor(ProviderContext{ReasoningBinding: "b"}, request, nil, nil); err != nil {
		t.Fatalf("EncodeProviderRequestFor() = %v", err)
	}
	if stream, err := path.NewStreamFor(ProviderContext{}, request); err != nil || stream == nil {
		t.Fatalf("NewStreamFor() = %v, %v", stream, err)
	}
	if _, _, err := path.DecodeProviderResponseFor(ProviderContext{}, request, nil); err != nil {
		t.Fatalf("DecodeProviderResponseFor() = %v", err)
	}
	if _, err := path.NewAggregatingStream(ProviderContext{}, request); !errors.Is(err, ErrCapabilityUnsupported) {
		t.Fatalf("NewAggregatingStream() = %v", err)
	}
	if _, _, ok := path.TranslateProviderRejection(request, 429, nil, nil); ok {
		t.Fatal("a New path translated a rejection")
	}
	if path.AdmitProviderRequest(request) != nil {
		t.Fatal("a New path refused a request")
	}
	if _, err := path.CountInputTokens(ProviderContext{}, request); !errors.Is(err, ErrCapabilityUnsupported) {
		t.Fatalf("CountInputTokens() = %v", err)
	}
	if _, err := path.RenderProviderError(request, protocolcore.ProviderError{Class: protocolcore.ProviderErrorServer}); !errors.Is(err, ErrCapabilityUnsupported) {
		t.Fatalf("RenderProviderError() = %v", err)
	}
	var nilPath *Path
	if nilPath.IsComposed() || nilPath.AggregatesUpstreamStream() {
		t.Fatal("a nil path reports composition")
	}
	if _, err := nilPath.NewStreamFor(ProviderContext{}, request); err == nil {
		t.Fatal("a nil path started a stream")
	}
}

func TestReasoningBindingIsAStableHash(t *testing.T) {
	t.Parallel()
	binding := NewReasoningBinding("target.openai.official", "account-1")
	if len(binding) != 32 || strings.Trim(binding, "0123456789abcdef") != "" {
		t.Fatalf("binding = %q", binding)
	}
	// SHA-256("vibermate/reasoning\x00target.openai.official\x00account-1").
	if binding != "1a61ba607653696e45666471c0bad6a3" {
		t.Fatalf("binding changed: %q", binding)
	}
	for _, other := range []string{
		NewReasoningBinding("target.openai.official", "account-2"),
		NewReasoningBinding("target.other", "account-1"),
		NewReasoningBinding("target.openai.official", ClientCredentialAccount),
		NewReasoningBinding("target.openai.officialaccount-1", ""),
	} {
		if other == binding {
			t.Fatal("different endpoints or accounts share a binding")
		}
	}
	seal := protocolcore.ReasoningSeal{Format: protocolcore.SealResponsesEncrypted, ItemID: "rs_1", Opaque: "ENC"}
	rejected := protocolcore.SealDigest(seal)
	memory := ProviderContext{RejectedSeal: func(digest [32]byte) bool { return digest == rejected }}
	if !memory.SealRejected(seal) {
		t.Fatal("the rejected-seal memory missed its seal")
	}
	seal.Opaque = "ENC-2"
	if memory.SealRejected(seal) || (ProviderContext{}).SealRejected(seal) {
		t.Fatal("an unrelated seal was reported as rejected")
	}
}
