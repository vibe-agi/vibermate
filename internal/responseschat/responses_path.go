package responseschat

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/vibe-agi/vibermate/internal/openairesponses"
	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

const (
	ResponsesPassthroughCodecPairID   = "openai-responses-original-passthrough"
	ResponsesPassthroughCodecRevision = 1
	ResponsesProviderRelativePath     = "v1/responses"
)

type responsesClientCodec struct {
	codec *openairesponses.Codec
}

func (responsesClientCodec) Dialect() protocolspec.Dialect {
	return protocolspec.DialectOpenAIResponses
}

func (codec responsesClientCodec) DecodeRequest(
	body []byte,
) (protocolcore.Request, protocolcore.TranslationReport, error) {
	request, _, err := codec.codec.DecodeCompatibleClientRequest(body)
	// The original same-dialect wire remains authoritative, so translation
	// notices about fields omitted by a Chat encoder do not apply here.
	return request, protocolcore.TranslationReport{}, err
}

func (codec responsesClientCodec) EncodeResponse(
	request protocolcore.Request,
	response protocolcore.Response,
) ([]byte, protocolcore.TranslationReport, error) {
	return codec.codec.EncodeClientResponse(request, response)
}

func (codec responsesClientCodec) EncodeSourceResponse(
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
	if len(sourceBody) == 0 || !json.Valid(sourceBody) {
		return nil, protocolcore.TranslationReport{},
			errors.New("Responses-compatible response body is invalid")
	}
	return bytes.Clone(sourceBody), protocolcore.TranslationReport{}, nil
}

type responsesBackendCodec struct {
	codec *openairesponses.Codec
}

func (responsesBackendCodec) Dialect() protocolspec.Dialect {
	return protocolspec.DialectOpenAIResponses
}

func (responsesBackendCodec) EncodeRequest(
	protocolcore.Request,
) (protocolpath.ProviderRequest, protocolcore.TranslationReport, error) {
	return protocolpath.ProviderRequest{}, protocolcore.TranslationReport{},
		errors.New("Responses-compatible provider encoding requires the validated source body")
}

func (codec responsesBackendCodec) EncodeSourceRequest(
	request protocolcore.Request,
	sourceBody []byte,
	_ http.Header,
) (protocolpath.ProviderRequest, protocolcore.TranslationReport, error) {
	if err := request.Validate(); err != nil {
		return protocolpath.ProviderRequest{}, protocolcore.TranslationReport{}, err
	}
	var root map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(sourceBody))
	if err := decoder.Decode(&root); err != nil || root == nil {
		return protocolpath.ProviderRequest{}, protocolcore.TranslationReport{},
			errors.New("Responses-compatible request body is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return protocolpath.ProviderRequest{}, protocolcore.TranslationReport{},
			errors.New("Responses-compatible request body has trailing data")
	}
	model, err := json.Marshal(request.EffectiveModel)
	if err != nil {
		return protocolpath.ProviderRequest{}, protocolcore.TranslationReport{}, err
	}
	root["model"] = model
	body, err := json.Marshal(root)
	if err != nil {
		return protocolpath.ProviderRequest{}, protocolcore.TranslationReport{}, err
	}
	headers := make(http.Header)
	if request.Stream {
		headers.Set("Accept", "text/event-stream")
	} else {
		headers.Set("Accept", "application/json")
	}
	encoded, err := protocolpath.NewProviderRequest(
		http.MethodPost,
		ResponsesProviderRelativePath,
		headers,
		body,
	)
	return encoded, protocolcore.TranslationReport{}, err
}

func (codec responsesBackendCodec) DecodeResponse(
	request protocolcore.Request,
	body []byte,
) (protocolcore.Response, protocolcore.TranslationReport, error) {
	return codec.codec.DecodeProviderResponse(request, body)
}

type responsesStreamingBridge struct {
	codec *openairesponses.Codec
}

func (bridge responsesStreamingBridge) NewStream(
	request protocolcore.Request,
) (protocolpath.Stream, error) {
	return bridge.codec.NewProviderStream(request)
}

var _ protocolpath.SourceRequestEncoder = responsesBackendCodec{}
var _ protocolpath.SourceResponseEncoder = responsesClientCodec{}
var _ protocolpath.StreamingBridge = responsesStreamingBridge{}
var _ protocolpath.Stream = (*openairesponses.ProviderStream)(nil)

func NewResponsesPassthroughProtocolPath(
	options openairesponses.Options,
) (*protocolpath.Path, error) {
	codec, err := openairesponses.New(options)
	if err != nil {
		return nil, err
	}
	identifier, err := protocolspec.NewCodecPairID(ResponsesPassthroughCodecPairID)
	if err != nil {
		return nil, err
	}
	operationIDs := make([]protocolspec.ClientOperationID, 0, 2)
	for _, raw := range []string{
		operationcatalog.OpenAIResponsesCreateID,
		operationcatalog.OpenAICodexResponsesCreateID,
	} {
		operationID, idErr := protocolspec.NewClientOperationID(raw)
		if idErr != nil {
			return nil, idErr
		}
		operationIDs = append(operationIDs, operationID)
	}
	return protocolpath.New(protocolpath.Options{
		ID:                 identifier,
		Revision:           ResponsesPassthroughCodecRevision,
		ClientOperationIDs: operationIDs,
		Client:             responsesClientCodec{codec: codec},
		Backend:            responsesBackendCodec{codec: codec},
		Streaming:          responsesStreamingBridge{codec: codec},
	})
}
