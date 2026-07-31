// Package responseschat explicitly composes the OpenAI Responses client edge
// with the existing OpenAI Chat backend edge. It owns no transport, Access
// selection, credentials, or global codec registry.
package responseschat

import (
	"context"
	"net/http"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/anthropicchat"
	"github.com/vibe-agi/vibermate/internal/openairesponses"
	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
)

const (
	CodecPairID   = "openai-responses-to-openai-chat"
	CodecRevision = 2
)

type Options struct {
	Responses openairesponses.Options
	Chat      anthropicchat.Options
}

func DefaultOptions() Options {
	chat := anthropicchat.DefaultOptions()
	chat.ProviderRequest =
		anthropicchat.SystemInstructionCompatibilityProfile()
	return Options{
		Responses: openairesponses.DefaultOptions(),
		Chat:      chat,
	}
}

type clientCodec struct {
	codec *openairesponses.Codec
}

func (clientCodec) Dialect() access.Dialect {
	return access.DialectOpenAIResponses
}

func (codec clientCodec) DecodeRequest(
	body []byte,
) (protocolcore.Request, protocolcore.TranslationReport, error) {
	return codec.codec.DecodeClientRequest(body)
}

func (codec clientCodec) EncodeResponse(
	request protocolcore.Request,
	response protocolcore.Response,
) ([]byte, protocolcore.TranslationReport, error) {
	return codec.codec.EncodeClientResponse(request, response)
}

type backendCodec struct {
	codec *anthropicchat.Codec
}

func (backendCodec) Dialect() access.Dialect {
	return access.DialectOpenAIChat
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
		anthropicchat.ProviderRelativePath,
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
	client *openairesponses.Codec
	chat   *anthropicchat.Codec
}

func (bridge streamingBridge) NewStream(
	request protocolcore.Request,
) (protocolpath.Stream, error) {
	encoder, err := bridge.client.NewStreamEncoder(request)
	if err != nil {
		return nil, err
	}
	stream, err := bridge.chat.NewProviderStreamWithEncoder(request, encoder)
	if err != nil {
		return nil, err
	}
	return streamAdapter{stream: stream}, nil
}

type streamAdapter struct {
	stream *anthropicchat.ProviderStream
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
	client, err := openairesponses.New(options.Responses)
	if err != nil {
		return nil, err
	}
	chat, err := anthropicchat.New(options.Chat)
	if err != nil {
		return nil, err
	}
	identifier, err := access.NewCodecPairID(CodecPairID)
	if err != nil {
		return nil, err
	}
	operationID, err := access.NewClientOperationID(
		operationcatalog.OpenAIResponsesCreateID,
	)
	if err != nil {
		return nil, err
	}
	return protocolpath.New(protocolpath.Options{
		ID:                 identifier,
		Revision:           access.Revision(CodecRevision),
		ClientOperationIDs: []access.ClientOperationID{operationID},
		Client:             clientCodec{codec: client},
		Backend:            backendCodec{codec: chat},
		Streaming:          streamingBridge{client: client, chat: chat},
	})
}
