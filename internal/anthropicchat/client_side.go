package anthropicchat

import (
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

// ClientSide is the Anthropic Messages client edge a composed path pairs with
// a provider side of another dialect (design 4.3). It decodes with the
// options' client input profile, encodes unary and streamed responses with
// thinking blocks, renders classified upstream rejections as the Anthropic
// errors Claude Code recovers from, and decodes count_tokens bodies. It never
// forwards source bytes, so it does not implement SourceResponseEncoder.
type ClientSide struct {
	codec *Codec
}

var _ protocolpath.ClientSide = (*ClientSide)(nil)

// NewClientSide builds the client side. A translated pair passes options
// whose ClientInput is TranslatedClientInputProfile().
func NewClientSide(options Options) (*ClientSide, error) {
	codec, err := New(options)
	if err != nil {
		return nil, err
	}
	return &ClientSide{codec: codec}, nil
}

func (*ClientSide) Dialect() protocolspec.Dialect {
	return protocolspec.DialectAnthropicMessages
}

func (side *ClientSide) DecodeRequest(body []byte) (protocolcore.Request, protocolcore.TranslationReport, error) {
	return side.codec.DecodeClientRequest(body)
}

// EncodeResponse writes one unary Anthropic message for request.
func (side *ClientSide) EncodeResponse(
	request protocolcore.Request,
	response protocolcore.Response,
) ([]byte, protocolcore.TranslationReport, error) {
	return side.codec.encodeClientResponse(request, response)
}

func (side *ClientSide) NewStreamEncoder(request protocolcore.Request) (protocolpath.ClientStreamEncoder, error) {
	return NewClientStreamEncoder(request)
}

func (side *ClientSide) RenderProviderError(
	request protocolcore.Request,
	providerError protocolcore.ProviderError,
) protocolcore.NativeProviderError {
	return RenderProviderError(request, providerError)
}

// DecodeCountTokensRequest decodes a count_tokens body for a local estimate.
func (side *ClientSide) DecodeCountTokensRequest(body []byte) (protocolcore.Request, protocolcore.TranslationReport, error) {
	return side.codec.DecodeCountTokensRequest(body)
}
