package anthropicchat

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

const (
	MessagesCodecPairID          = "anthropic-messages-to-anthropic-messages"
	MessagesCodecRevision        = 1
	MessagesProviderRelativePath = "v1/messages"
	AnthropicVersion             = "2023-06-01"
)

// messagesClientCodec validates Anthropic client semantics while preserving
// the compatible wire on the response side. It is deliberately separate from
// clientCodec: the Anthropic-to-OpenAI path must continue to encode translated
// responses rather than returning an OpenAI body to an Anthropic client.
type messagesClientCodec struct {
	codec *Codec
}

func (messagesClientCodec) Dialect() protocolspec.Dialect {
	return protocolspec.DialectAnthropicMessages
}

func (codec messagesClientCodec) DecodeRequest(
	body []byte,
) (protocolcore.Request, protocolcore.TranslationReport, error) {
	request, _, err := codec.codec.DecodeCompatibleClientRequest(body)
	// Same-dialect forwarding preserves compatible extensions in the source
	// body, so the cross-dialect "not forwarded" notices do not apply here.
	return request, protocolcore.TranslationReport{}, err
}

func (codec messagesClientCodec) EncodeResponse(
	_ protocolcore.Request,
	response protocolcore.Response,
) ([]byte, protocolcore.TranslationReport, error) {
	encoded, err := codec.codec.EncodeClientResponse(response)
	return encoded, protocolcore.TranslationReport{}, err
}

func (codec messagesClientCodec) EncodeSourceResponse(
	request protocolcore.Request,
	response protocolcore.Response,
	sourceBody []byte,
) ([]byte, protocolcore.TranslationReport, error) {
	if err := request.Validate(); err != nil {
		return nil, protocolcore.TranslationReport{}, err
	}
	if err := response.Validate(); err != nil {
		return nil, protocolcore.TranslationReport{}, err
	}
	if len(sourceBody) == 0 ||
		len(sourceBody) > codec.codec.options.MaxResponseBytes ||
		!json.Valid(sourceBody) {
		return nil, protocolcore.TranslationReport{},
			errors.New("Anthropic-compatible response body is invalid")
	}
	if request.RequestedModel == request.EffectiveModel {
		return bytes.Clone(sourceBody), protocolcore.TranslationReport{}, nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(sourceBody, &root); err != nil || root == nil {
		return nil, protocolcore.TranslationReport{},
			errors.New("Anthropic-compatible response body is invalid")
	}
	var reportedModel string
	if err := json.Unmarshal(root["model"], &reportedModel); err != nil ||
		reportedModel == "" {
		return nil, protocolcore.TranslationReport{},
			errors.New("Anthropic-compatible response model is invalid")
	}
	if reportedModel == request.RequestedModel {
		return bytes.Clone(sourceBody), protocolcore.TranslationReport{}, nil
	}
	model, err := json.Marshal(request.RequestedModel)
	if err != nil {
		return nil, protocolcore.TranslationReport{}, err
	}
	root["model"] = model
	encoded, err := json.Marshal(root)
	if err != nil {
		return nil, protocolcore.TranslationReport{}, err
	}
	return encoded, protocolcore.TranslationReport{}, nil
}

type messagesBackendCodec struct {
	codec *Codec
}

func (messagesBackendCodec) Dialect() protocolspec.Dialect {
	return protocolspec.DialectAnthropicMessages
}

func (codec messagesBackendCodec) EncodeRequest(
	_ protocolcore.Request,
) (protocolpath.ProviderRequest, protocolcore.TranslationReport, error) {
	return protocolpath.ProviderRequest{}, protocolcore.TranslationReport{},
		errors.New("Anthropic-compatible provider encoding requires the validated source body")
}

func (codec messagesBackendCodec) EncodeSourceRequest(
	request protocolcore.Request,
	sourceBody []byte,
	sourceHeaders http.Header,
) (protocolpath.ProviderRequest, protocolcore.TranslationReport, error) {
	if err := request.Validate(); err != nil {
		return protocolpath.ProviderRequest{}, protocolcore.TranslationReport{},
			protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
	}
	if len(sourceBody) == 0 || len(sourceBody) > codec.codec.options.MaxRequestBytes {
		return protocolpath.ProviderRequest{}, protocolcore.TranslationReport{},
			protocolcore.NewFailure(
				protocolcore.ReasonInvalidClientRequest,
				"$",
				errors.New("request body has an invalid size"),
			)
	}
	var root map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(sourceBody))
	if err := decoder.Decode(&root); err != nil || root == nil {
		return protocolpath.ProviderRequest{}, protocolcore.TranslationReport{},
			protocolcore.NewFailure(
				protocolcore.ReasonInvalidClientRequest,
				"$",
				errors.New("request body is not a JSON object"),
			)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return protocolpath.ProviderRequest{}, protocolcore.TranslationReport{},
			protocolcore.NewFailure(
				protocolcore.ReasonInvalidClientRequest,
				"$",
				errors.New("request body has trailing data"),
			)
	}
	model, err := json.Marshal(request.EffectiveModel)
	if err != nil {
		return protocolpath.ProviderRequest{}, protocolcore.TranslationReport{}, err
	}
	root["model"] = model
	encoded, err := json.Marshal(root)
	if err != nil {
		return protocolpath.ProviderRequest{}, protocolcore.TranslationReport{}, err
	}
	headers := make(http.Header)
	if request.Stream {
		headers.Set("Accept", "text/event-stream")
	} else {
		headers.Set("Accept", "application/json")
	}
	headers.Set("Anthropic-Version", AnthropicVersion)
	if beta := sourceHeaders.Get("Anthropic-Beta"); beta != "" {
		headers.Set("Anthropic-Beta", beta)
	}
	providerRequest, err := protocolpath.NewProviderRequest(
		http.MethodPost,
		MessagesProviderRelativePath,
		headers,
		encoded,
	)
	return providerRequest, protocolcore.TranslationReport{}, err
}

func (codec messagesBackendCodec) DecodeResponse(
	request protocolcore.Request,
	body []byte,
) (protocolcore.Response, protocolcore.TranslationReport, error) {
	response, err := codec.codec.DecodeAnthropicProviderResponse(request, body)
	return response, protocolcore.TranslationReport{}, err
}

type messagesStreamingBridge struct {
	codec *Codec
}

func (bridge messagesStreamingBridge) NewStream(
	request protocolcore.Request,
) (protocolpath.Stream, error) {
	return bridge.codec.NewAnthropicProviderStream(request)
}

func NewMessagesProtocolPath(options Options) (*protocolpath.Path, error) {
	codec, err := New(options)
	if err != nil {
		return nil, err
	}
	identifier, err := protocolspec.NewCodecPairID(MessagesCodecPairID)
	if err != nil {
		return nil, err
	}
	operationID, err := protocolspec.NewClientOperationID(
		operationcatalog.AnthropicMessagesCreateID,
	)
	if err != nil {
		return nil, err
	}
	path, err := protocolpath.New(protocolpath.Options{
		ID:                 identifier,
		Revision:           protocolspec.Revision(MessagesCodecRevision),
		ClientOperationIDs: []protocolspec.ClientOperationID{operationID},
		Client:             messagesClientCodec{codec: codec},
		Backend:            messagesBackendCodec{codec: codec},
		Streaming:          messagesStreamingBridge{codec: codec},
	})
	if err != nil {
		return nil, fmt.Errorf("build Anthropic-compatible protocol path: %w", err)
	}
	return path, nil
}
