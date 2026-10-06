// Package openairesponses implements the trusted OpenAI Responses client wire
// edge. It owns no transport, Environment selection, credentials, or global codec
// registry.
package openairesponses

import (
	"errors"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

const (
	SourceOpenAIResponses = "openai-responses"
	MaxMetadataBytes      = 1 << 20
)

type Options struct {
	MaxRequestBytes  int
	MaxResponseBytes int
	Resources        *protocolcore.ResourceLimits
}

func DefaultOptions() Options {
	return Options{
		MaxRequestBytes:  16 << 20,
		MaxResponseBytes: 16 << 20,
	}
}

type Codec struct {
	options Options
}

func New(options Options) (*Codec, error) {
	if options.MaxRequestBytes <= 0 || options.MaxResponseBytes <= 0 {
		return nil, errors.New("Responses codec limits must be positive")
	}
	if options.Resources != nil {
		if err := options.Resources.Validate(); err != nil {
			return nil, err
		}
		limits := *options.Resources
		options.Resources = &limits
	}
	return &Codec{options: options}, nil
}

// ValidateRequest applies this codec's immutable constructor policy.
func (codec *Codec) ValidateRequest(request protocolcore.Request) error {
	if codec.options.Resources != nil {
		return protocolcore.ValidateRequestWithin(request, *codec.options.Resources)
	}
	return request.Validate()
}

func (codec *Codec) ValidateResponse(response protocolcore.Response) error {
	if codec.options.Resources != nil {
		return protocolcore.ValidateResponseWithin(response, *codec.options.Resources)
	}
	return response.Validate()
}

// Preflight is before duplicate-name maps and root unmarshal, not a substitute
// for the final complete semantic validation before an owned clone.
// ValidateRequestJSON preflights source bytes/resources and JSON names, not
// dialect semantics. Nil preserves the existing source-encoder behavior.
func (codec *Codec) ValidateRequestJSON(body []byte) error {
	if codec.options.Resources != nil {
		if len(body) == 0 || len(body) > codec.options.MaxRequestBytes {
			return errors.New("request body has an invalid size")
		}
		return protocolcore.ValidateJSONWithin(body, codec.options.Resources.Request)
	}
	return nil
}

// ValidateResponseJSON bounds the incoming provider source, separately from
// the size of a subsequently re-encoded client response.
func (codec *Codec) ValidateResponseJSON(body []byte) error {
	if codec.options.Resources != nil {
		if len(body) == 0 || len(body) > codec.options.MaxResponseBytes {
			return errors.New("response body has an invalid size")
		}
		return protocolcore.ValidateJSONWithin(body, codec.options.Resources.Response)
	}
	return nil
}

func (codec *Codec) requestBudget() *protocolcore.ResourceBudget {
	if codec.options.Resources == nil {
		return nil
	}
	budget, _ := protocolcore.NewResourceBudget(codec.options.Resources.Request)
	return budget
}

func (codec *Codec) responseBudget() *protocolcore.ResourceBudget {
	if codec.options.Resources == nil {
		return nil
	}
	budget, _ := protocolcore.NewResourceBudget(codec.options.Resources.Response)
	return budget
}

func invalidClient(path string, cause error) error {
	return protocolcore.NewFailure(
		protocolcore.ReasonInvalidClientRequest,
		path,
		cause,
	)
}
