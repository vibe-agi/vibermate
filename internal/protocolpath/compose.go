package protocolpath

import (
	"context"
	"errors"
	"net/http"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

// ErrCapabilityUnsupported is returned by a Path method whose capability the
// path does not offer.
var ErrCapabilityUnsupported = errors.New("protocol path does not offer this capability")

// ClientSide is one dialect's client wire edge, reusable with any provider
// side of another dialect.
type ClientSide interface {
	ClientCodec
	NewStreamEncoder(protocolcore.Request) (ClientStreamEncoder, error)
	// RenderProviderError renders a classified upstream rejection as a native
	// error of the client's dialect, choosing the client status when the
	// client recognises a recovery only on a particular one.
	RenderProviderError(protocolcore.Request, protocolcore.ProviderError) protocolcore.NativeProviderError
}

// ProviderSide is one dialect's provider wire edge, reusable with any client
// side of another dialect. EncodeRequest and DecodeResponse behave as their
// ...For forms with a zero ProviderContext.
type ProviderSide interface {
	BackendCodec
	// TransformedRequestAdmission re-admits the post-script provider body.
	TransformedRequestAdmission
	EncodeRequestFor(ProviderContext, protocolcore.Request) (ProviderRequest, protocolcore.TranslationReport, error)
	DecodeResponseFor(ProviderContext, protocolcore.Request, []byte) (protocolcore.Response, protocolcore.TranslationReport, error)
	// NewProviderStream drives the client encoder it is given.
	NewProviderStream(ProviderContext, protocolcore.Request, ClientStreamEncoder) (Stream, error)
	// NewAggregatingStream accepts a Stream=false request; its Feed never
	// returns bytes and FinishDecoded gives the whole response.
	NewAggregatingStream(ProviderContext, protocolcore.Request) (Stream, error)
	// ClassifyRejection reads an upstream HTTP rejection. It never keeps
	// provider text.
	ClassifyRejection(protocolcore.Request, int, http.Header, []byte) protocolcore.ProviderError
}

// ProviderRequestAdmission is an optional provider-side capability: it
// refuses, right after client decode, a request the provider cannot carry.
type ProviderRequestAdmission interface {
	AdmitRequest(protocolcore.Request) *protocolcore.ProviderError
}

// TokenCounter is an optional provider-side capability: an input-token
// estimate of exactly what the provider side would send.
type TokenCounter interface {
	CountInputTokens(ProviderContext, protocolcore.Request) (int, error)
}

type ComposeOptions struct {
	ID                 protocolspec.CodecPairID
	Revision           protocolspec.Revision
	ClientOperationIDs []protocolspec.ClientOperationID
	Client             ClientSide
	Provider           ProviderSide
}

// composition holds the two sides of a composed path.
type composition struct {
	client   ClientSide
	provider ProviderSide
}

// Compose joins a client side and a provider side of different dialects into
// the same *Path that New builds. Same-dialect pairs keep forwarding the
// source wire, so Compose refuses them, and it refuses sides that implement
// the source-wire encoders a composed path must never use.
func Compose(options ComposeOptions) (*Path, error) {
	if options.Client == nil || options.Provider == nil {
		return nil, errors.New("protocol path dependencies are incomplete")
	}
	if options.Client.Dialect() == options.Provider.Dialect() {
		return nil, errors.New("composed protocol path sides share a dialect")
	}
	if _, source := options.Client.(SourceResponseEncoder); source {
		return nil, errors.New("composed client side forwards source responses")
	}
	if _, source := options.Provider.(SourceRequestEncoder); source {
		return nil, errors.New("composed provider side forwards source requests")
	}
	sides := &composition{client: options.Client, provider: options.Provider}
	path, err := New(Options{
		ID:                 options.ID,
		Revision:           options.Revision,
		ClientOperationIDs: options.ClientOperationIDs,
		Client:             options.Client,
		Backend:            options.Provider,
		Streaming:          composedBridge{sides: sides},
	})
	if err != nil {
		return nil, err
	}
	path.composition = sides
	return path, nil
}

// composedBridge serves callers of Streaming().NewStream with a zero
// ProviderContext.
type composedBridge struct {
	sides *composition
}

func (bridge composedBridge) NewStream(request protocolcore.Request) (Stream, error) {
	return bridge.sides.newStream(ProviderContext{}, request)
}

func (sides *composition) newStream(providerContext ProviderContext, request protocolcore.Request) (Stream, error) {
	encoder, err := sides.client.NewStreamEncoder(request)
	if err != nil {
		return nil, err
	}
	stream, err := sides.provider.NewProviderStream(providerContext, request, encoder)
	if err != nil {
		return nil, err
	}
	return composedStream{stream: stream, client: sides.client, request: request}, nil
}

// composedStream renders a classified provider failure as the client side's
// native error, so the existing abort path writes it in the client dialect.
type composedStream struct {
	stream  Stream
	client  ClientSide
	request protocolcore.Request
}

func (stream composedStream) Feed(ctx context.Context, fragment []byte) ([]byte, error) {
	output, err := stream.stream.Feed(ctx, fragment)
	return output, stream.render(err)
}

func (stream composedStream) SemanticProgress() uint64 { return stream.stream.SemanticProgress() }

func (stream composedStream) TerminalReceived() bool { return stream.stream.TerminalReceived() }

func (stream composedStream) FinishDecoded(ctx context.Context) (PendingTerminal, error) {
	terminal, err := stream.stream.FinishDecoded(ctx)
	return terminal, stream.render(err)
}

func (stream composedStream) render(err error) error {
	var failure *protocolcore.Failure
	if err == nil || !errors.As(err, &failure) || failure.Provider == nil ||
		failure.Provider.Validate() != nil {
		return err
	}
	native := stream.client.RenderProviderError(stream.request, *failure.Provider)
	if native.ForDialect(stream.client.Dialect()) != nil {
		failure.NativeError = native
	}
	return err
}

// IsComposed reports a path built with Compose.
func (path *Path) IsComposed() bool {
	return path != nil && path.composition != nil
}

// EncodeProviderRequestFor encodes for the frozen target of one attempt. A
// path built with New ignores the context.
func (path *Path) EncodeProviderRequestFor(
	providerContext ProviderContext,
	request protocolcore.Request,
	sourceBody []byte,
	sourceHeaders http.Header,
) (ProviderRequest, protocolcore.TranslationReport, error) {
	if path.IsComposed() {
		return path.composition.provider.EncodeRequestFor(providerContext, request)
	}
	return path.EncodeProviderRequest(request, sourceBody, sourceHeaders)
}

// NewStreamFor starts the stream decoder of one attempt.
func (path *Path) NewStreamFor(providerContext ProviderContext, request protocolcore.Request) (Stream, error) {
	if path == nil {
		return nil, errors.New("protocol path is unavailable")
	}
	if path.IsComposed() {
		return path.composition.newStream(providerContext, request)
	}
	return path.streaming.NewStream(request)
}

// DecodeProviderResponseFor decodes a unary provider response of one attempt.
func (path *Path) DecodeProviderResponseFor(
	providerContext ProviderContext,
	request protocolcore.Request,
	body []byte,
) (protocolcore.Response, protocolcore.TranslationReport, error) {
	if path.IsComposed() {
		return path.composition.provider.DecodeResponseFor(providerContext, request, body)
	}
	if path == nil || path.backend == nil {
		return protocolcore.Response{}, protocolcore.TranslationReport{},
			errors.New("protocol path backend is unavailable")
	}
	return path.backend.DecodeResponse(request, body)
}

// AggregatesUpstreamStream reports a path that always streams upstream and
// aggregates the stream for a Stream=false client.
func (path *Path) AggregatesUpstreamStream() bool {
	return path.IsComposed()
}

// NewAggregatingStream starts the aggregating decoder of a Stream=false
// request on a path that AggregatesUpstreamStream.
func (path *Path) NewAggregatingStream(providerContext ProviderContext, request protocolcore.Request) (Stream, error) {
	if !path.IsComposed() {
		return nil, ErrCapabilityUnsupported
	}
	stream, err := path.composition.provider.NewAggregatingStream(providerContext, request)
	if err != nil {
		return nil, err
	}
	return composedStream{stream: stream, client: path.composition.client, request: request}, nil
}

// TranslateProviderRejection classifies an upstream HTTP rejection and
// renders it as a native error of the client's dialect. ok is false, and the
// caller keeps today's generic envelope, when the path is not composed or
// either side cannot produce its half.
func (path *Path) TranslateProviderRejection(
	request protocolcore.Request,
	status int,
	header http.Header,
	body []byte,
) (native protocolcore.NativeProviderError, class protocolcore.ProviderError, ok bool) {
	if !path.IsComposed() {
		return protocolcore.NativeProviderError{}, protocolcore.ProviderError{}, false
	}
	class = path.composition.provider.ClassifyRejection(request, status, header, body)
	if class.Validate() != nil {
		return protocolcore.NativeProviderError{}, protocolcore.ProviderError{}, false
	}
	native = path.composition.client.RenderProviderError(request, class)
	if native.ForDialect(path.composition.client.Dialect()) == nil {
		return protocolcore.NativeProviderError{}, protocolcore.ProviderError{}, false
	}
	return native, class, true
}

// AdmitProviderRequest lets a composed provider side refuse, right after
// client decode, a request it cannot carry. nil admits.
func (path *Path) AdmitProviderRequest(request protocolcore.Request) *protocolcore.ProviderError {
	if !path.IsComposed() {
		return nil
	}
	admission, ok := path.composition.provider.(ProviderRequestAdmission)
	if !ok {
		return nil
	}
	return admission.AdmitRequest(request)
}

// CountInputTokens estimates the input tokens the provider side would send.
func (path *Path) CountInputTokens(providerContext ProviderContext, request protocolcore.Request) (int, error) {
	if !path.IsComposed() {
		return 0, ErrCapabilityUnsupported
	}
	counter, ok := path.composition.provider.(TokenCounter)
	if !ok {
		return 0, ErrCapabilityUnsupported
	}
	return counter.CountInputTokens(providerContext, request)
}

// RenderProviderError renders a classified failure in the client's dialect.
func (path *Path) RenderProviderError(
	request protocolcore.Request,
	providerError protocolcore.ProviderError,
) (protocolcore.NativeProviderError, error) {
	if !path.IsComposed() {
		return protocolcore.NativeProviderError{}, ErrCapabilityUnsupported
	}
	if err := providerError.Validate(); err != nil {
		return protocolcore.NativeProviderError{}, err
	}
	return path.composition.client.RenderProviderError(request, providerError), nil
}
