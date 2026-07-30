// Package protocolpath defines the typed boundary between one immutable Access
// codec plan and the two trusted wire edges that execute it. It owns no
// transport, routing, credentials, listener, or global codec registry.
package protocolpath

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

type ClientCodec interface {
	Dialect() access.Dialect
	DecodeRequest(
		[]byte,
	) (protocolcore.Request, protocolcore.TranslationReport, error)
	EncodeResponse(protocolcore.Response) ([]byte, error)
}

type BackendCodec interface {
	Dialect() access.Dialect
	EncodeRequest(
		protocolcore.Request,
	) (ProviderRequest, protocolcore.TranslationReport, error)
	DecodeResponse(
		protocolcore.Request,
		[]byte,
	) (protocolcore.Response, protocolcore.TranslationReport, error)
}

type ProviderRequest struct {
	method       string
	relativePath string
	headers      http.Header
	body         []byte
}

func NewProviderRequest(
	method string,
	relativePath string,
	headers http.Header,
	body []byte,
) (ProviderRequest, error) {
	if method == "" ||
		strings.TrimSpace(method) != method ||
		relativePath == "" ||
		strings.HasPrefix(relativePath, "/") ||
		strings.ContainsAny(relativePath, "?# \t\r\n") ||
		len(body) == 0 {
		return ProviderRequest{}, errors.New(
			"encoded provider request is invalid",
		)
	}
	return ProviderRequest{
		method:       method,
		relativePath: relativePath,
		headers:      headers.Clone(),
		body:         append([]byte(nil), body...),
	}, nil
}

func (request ProviderRequest) Method() string {
	return request.method
}

func (request ProviderRequest) RelativePath() string {
	return request.relativePath
}

func (request ProviderRequest) Headers() http.Header {
	return request.headers.Clone()
}

func (request ProviderRequest) Body() []byte {
	return append([]byte(nil), request.body...)
}

type Stream interface {
	Feed(context.Context, []byte) ([]byte, error)
	SemanticProgress() uint64
	FinishDecoded(context.Context) (PendingTerminal, error)
}

type PendingTerminal interface {
	ToolIntents() []protocolcore.ToolIntent
	DecodedResponse() protocolcore.Response
	TranslationReport() protocolcore.TranslationReport
	Approve() ([]byte, error)
	Reject() error
}

// StreamingBridge is an explicitly versioned compatibility seam for a stream
// whose backend decoder and client encoder must coordinate block ordering and
// the complete-tool barrier. It does not own SSE transport reads or writes.
type StreamingBridge interface {
	NewStream(protocolcore.Request) (Stream, error)
}

type Options struct {
	ID        access.CodecPairID
	Revision  access.Revision
	Client    ClientCodec
	Backend   BackendCodec
	Streaming StreamingBridge
}

// Path is one immutable, explicitly assembled codec capability. It contains
// two independent wire edges joined by protocolcore IR and no string registry.
type Path struct {
	id        access.CodecPairID
	revision  access.Revision
	client    ClientCodec
	backend   BackendCodec
	streaming StreamingBridge
}

func New(options Options) (*Path, error) {
	if options.ID.String() == "" ||
		options.Revision == 0 ||
		options.Client == nil ||
		options.Backend == nil ||
		options.Streaming == nil ||
		options.Client.Dialect() == "" ||
		options.Backend.Dialect() == "" {
		return nil, errors.New("protocol path dependencies are incomplete")
	}
	return &Path{
		id:        options.ID,
		revision:  options.Revision,
		client:    options.Client,
		backend:   options.Backend,
		streaming: options.Streaming,
	}, nil
}

func (path *Path) Client() ClientCodec {
	if path == nil {
		return nil
	}
	return path.client
}

func (path *Path) Backend() BackendCodec {
	if path == nil {
		return nil
	}
	return path.backend
}

func (path *Path) Streaming() StreamingBridge {
	if path == nil {
		return nil
	}
	return path.streaming
}

func (path *Path) ValidatePlan(plan access.CodecPlan) error {
	if path == nil ||
		plan.ID() != path.id ||
		plan.Revision() != path.revision ||
		plan.ClientDialect() != path.client.Dialect() ||
		plan.ProviderDialect() != path.backend.Dialect() {
		return errors.New("active Access codec plan is unsupported")
	}
	return nil
}
