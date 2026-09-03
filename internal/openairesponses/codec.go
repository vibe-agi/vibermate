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
	return &Codec{options: options}, nil
}

func invalidClient(path string, cause error) error {
	return protocolcore.NewFailure(
		protocolcore.ReasonInvalidClientRequest,
		path,
		cause,
	)
}
