package anthropicchat

import (
	"context"
	"net/http"

	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

type clientCodec struct {
	codec *Codec
}

func (clientCodec) Dialect() protocolspec.Dialect {
	return protocolspec.DialectAnthropicMessages
}

func (codec clientCodec) DecodeRequest(
	body []byte,
) (protocolcore.Request, protocolcore.TranslationReport, error) {
	return codec.codec.DecodeClientRequest(body)
}

func (codec clientCodec) EncodeResponse(
	_ protocolcore.Request,
	response protocolcore.Response,
) ([]byte, protocolcore.TranslationReport, error) {
	encoded, err := codec.codec.EncodeClientResponse(response)
	return encoded, protocolcore.TranslationReport{}, err
}

type backendCodec struct {
	codec *Codec
}

func (backendCodec) Dialect() protocolspec.Dialect {
	return protocolspec.DialectOpenAIChat
}

func (codec backendCodec) EncodeRequest(
	request protocolcore.Request,
) (protocolpath.ProviderRequest, protocolcore.TranslationReport, error) {
	body, report, err := codec.codec.EncodeProviderRequest(request)
	if err != nil {
		return protocolpath.ProviderRequest{}, report, err
	}
	headers := make(http.Header)
	if request.Stream {
		headers.Set("Accept", "text/event-stream")
	} else {
		headers.Set("Accept", "application/json")
	}
	encoded, err := protocolpath.NewProviderRequest(
		http.MethodPost,
		ProviderRelativePath,
		headers,
		body,
	)
	return encoded, report, err
}

func (codec backendCodec) DecodeResponse(
	request protocolcore.Request,
	body []byte,
) (protocolcore.Response, protocolcore.TranslationReport, error) {
	return codec.codec.DecodeProviderResponse(request, body)
}

type streamingBridge struct {
	codec *Codec
}

func (bridge streamingBridge) NewStream(
	request protocolcore.Request,
) (protocolpath.Stream, error) {
	stream, err := bridge.codec.NewProviderStream(request)
	if err != nil {
		return nil, err
	}
	return streamAdapter{stream: stream}, nil
}

type streamAdapter struct {
	stream *ProviderStream
}

func (adapter streamAdapter) Feed(
	ctx context.Context,
	fragment []byte,
) ([]byte, error) {
	return adapter.stream.Feed(ctx, fragment)
}

func (adapter streamAdapter) SemanticProgress() uint64 {
	return adapter.stream.SemanticProgress()
}

func (adapter streamAdapter) FinishDecoded(
	ctx context.Context,
) (protocolpath.PendingTerminal, error) {
	terminal, err := adapter.stream.FinishDecoded(ctx)
	if err != nil {
		return nil, err
	}
	return terminal, nil
}

func NewProtocolPath(options Options) (*protocolpath.Path, error) {
	codec, err := New(options)
	if err != nil {
		return nil, err
	}
	identifier, err := protocolspec.NewCodecPairID(CodecPairID)
	if err != nil {
		return nil, err
	}
	operationID, err := protocolspec.NewClientOperationID(
		operationcatalog.AnthropicMessagesCreateID,
	)
	if err != nil {
		return nil, err
	}
	return protocolpath.New(protocolpath.Options{
		ID:                 identifier,
		Revision:           protocolspec.Revision(CodecRevision),
		ClientOperationIDs: []protocolspec.ClientOperationID{operationID},
		Client:             clientCodec{codec: codec},
		Backend:            backendCodec{codec: codec},
		Streaming:          streamingBridge{codec: codec},
	})
}
