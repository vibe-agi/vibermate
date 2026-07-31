// Package protocolpath defines the typed boundary between one immutable Access
// codec plan and the two trusted wire edges that execute it. It owns no
// transport, routing, credentials, listener, or global codec registry.
package protocolpath

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

type ClientCodec interface {
	Dialect() access.Dialect
	DecodeRequest(
		[]byte,
	) (protocolcore.Request, protocolcore.TranslationReport, error)
	EncodeResponse(
		protocolcore.Request,
		protocolcore.Response,
	) ([]byte, protocolcore.TranslationReport, error)
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
	ID                 access.CodecPairID
	Revision           access.Revision
	ClientOperationIDs []access.ClientOperationID
	Client             ClientCodec
	Backend            BackendCodec
	Streaming          StreamingBridge
}

// Path is one immutable, explicitly assembled codec capability. It contains
// two independent wire edges joined by protocolcore IR and no string registry.
type Path struct {
	id         access.CodecPairID
	revision   access.Revision
	operations []access.ClientOperationID
	client     ClientCodec
	backend    BackendCodec
	streaming  StreamingBridge
}

func New(options Options) (*Path, error) {
	if options.ID.String() == "" ||
		options.Revision == 0 ||
		len(options.ClientOperationIDs) == 0 ||
		options.Client == nil ||
		options.Backend == nil ||
		options.Streaming == nil ||
		options.Client.Dialect() == "" ||
		options.Backend.Dialect() == "" {
		return nil, errors.New("protocol path dependencies are incomplete")
	}
	operations := slices.Clone(options.ClientOperationIDs)
	sort.Slice(operations, func(left, right int) bool {
		return operations[left].String() < operations[right].String()
	})
	for index, operationID := range operations {
		if operationID.String() == "" ||
			(index > 0 && operationID == operations[index-1]) {
			return nil, errors.New(
				"protocol path client operations are invalid",
			)
		}
	}
	return &Path{
		id:         options.ID,
		revision:   options.Revision,
		operations: operations,
		client:     options.Client,
		backend:    options.Backend,
		streaming:  options.Streaming,
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

func (path *Path) ClientOperationIDs() []access.ClientOperationID {
	if path == nil {
		return nil
	}
	return slices.Clone(path.operations)
}

func (path *Path) SupportsClientOperation(
	operationID access.ClientOperationID,
) bool {
	if path == nil || operationID.String() == "" {
		return false
	}
	return slices.Contains(path.operations, operationID)
}

func (path *Path) ValidatePlan(plan access.CodecPlan) error {
	if path == nil ||
		plan.ID() != path.id ||
		plan.Revision() != path.revision ||
		plan.ClientDialect() != path.client.Dialect() ||
		plan.ProviderDialect() != path.backend.Dialect() {
		return errors.New("active Access codec plan is unsupported")
	}
	planOperations := plan.ClientOperations()
	if len(planOperations) != len(path.operations) {
		return errors.New("active Access client operations are unsupported")
	}
	operationIDs := make([]access.ClientOperationID, len(planOperations))
	for index, operation := range planOperations {
		operationIDs[index] = operation.ID()
	}
	sort.Slice(operationIDs, func(left, right int) bool {
		return operationIDs[left].String() < operationIDs[right].String()
	})
	if !slices.Equal(operationIDs, path.operations) {
		return errors.New("active Access client operations are unsupported")
	}
	return nil
}

// Selector is an immutable, explicitly assembled set of typed protocol paths.
// It performs no registration and owns no global state.
type Selector struct {
	paths []*Path
}

func NewSelector(paths ...*Path) (*Selector, error) {
	if len(paths) == 0 {
		return nil, errors.New("protocol path selector is empty")
	}
	owned := slices.Clone(paths)
	for index, path := range owned {
		if path == nil {
			return nil, errors.New("protocol path selector contains a nil path")
		}
		for previous := 0; previous < index; previous++ {
			if owned[previous].id == path.id &&
				owned[previous].revision == path.revision {
				return nil, errors.New(
					"protocol path selector contains a duplicate path",
				)
			}
		}
	}
	return &Selector{paths: owned}, nil
}

func (selector *Selector) Select(
	plan access.CodecPlan,
	operationID access.ClientOperationID,
) (*Path, error) {
	if selector == nil || operationID.String() == "" {
		return nil, errors.New("protocol path selection input is invalid")
	}
	for _, path := range selector.paths {
		if path.ValidatePlan(plan) != nil {
			continue
		}
		if !path.SupportsClientOperation(operationID) {
			return nil, errors.New(
				"active Access client operation is unsupported",
			)
		}
		return path, nil
	}
	return nil, errors.New("active Access codec plan is unsupported")
}
