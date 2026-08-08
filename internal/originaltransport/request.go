package originaltransport

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

const maxOriginalRequestBytes = 16 << 20

type RequestOptions struct {
	RequestID    string
	Kind         offlinehold.EgressKind
	Origin       originidentity.ClientOrigin
	Method       string
	Path         string
	RawQuery     string
	Headers      http.Header
	Body         []byte
	PayloadClass protocolspec.OperationPayloadClass
	// ConnectionID and ParentID associate this outbound with the client
	// connection and the original request that caused it. They travel as typed
	// references so no identity encodes containment of another.
	ConnectionID string
	ParentID     string
}

// Request freezes an original-origin auxiliary or opaque representation. It
// cannot replace its ClientOrigin with a provider target or arbitrary URL.
type Request struct {
	requestID    string
	kind         offlinehold.EgressKind
	origin       originidentity.ClientOrigin
	method       string
	path         string
	rawQuery     string
	headers      http.Header
	body         []byte
	payloadClass protocolspec.OperationPayloadClass
	connectionID string
	parentID     string
}

func NewRequest(options RequestOptions) (Request, error) {
	if err := validateIdentity("request ID", options.RequestID); err != nil {
		return Request{}, err
	}
	switch options.Kind {
	case offlinehold.EgressAuxiliary, offlinehold.EgressOpaque:
	default:
		return Request{}, errors.New("original-origin egress kind is invalid")
	}
	// This is the last boundary before the client's own credential leaves the
	// process, so the invariant is re-proved here instead of trusting the
	// caller: an original-origin request never carries client payload.
	//
	// A catalogued none/control operation is proven to hold no prompt, tool, or
	// document data, so it may carry its own small control body. An
	// unclassified operation has no such proof and is admitted only when its
	// empty body establishes payload-freedom on its own; the connection-policy
	// Goal replaces that narrow exception with an explicit allow/deny/ask
	// decision.
	switch options.PayloadClass {
	case protocolspec.OperationPayloadNone, protocolspec.OperationPayloadControl:
	case protocolspec.OperationPayloadUnknown:
		if len(options.Body) > 0 {
			return Request{}, errors.New(
				"unclassified original-origin request cannot carry a body",
			)
		}
	default:
		return Request{}, fmt.Errorf(
			"original-origin transport refuses payload class %q",
			options.PayloadClass,
		)
	}
	if err := validateIdentity(
		"original-origin connection ID",
		options.ConnectionID,
	); err != nil {
		return Request{}, err
	}
	if err := validateIdentity(
		"original-origin parent ID",
		options.ParentID,
	); err != nil {
		return Request{}, err
	}
	if err := options.Origin.Validate(); err != nil {
		return Request{}, errors.New("original ClientOrigin is incomplete")
	}
	if options.Method == "" ||
		strings.ToUpper(options.Method) != options.Method ||
		options.Path == "" ||
		options.Path[0] != '/' ||
		strings.ContainsAny(options.Path, "%?#\\\r\n") ||
		strings.ContainsAny(options.RawQuery, "#\r\n") ||
		len(options.Body) > maxOriginalRequestBytes {
		return Request{}, errors.New("original-origin request target is invalid")
	}
	return Request{
		requestID:    options.RequestID,
		kind:         options.Kind,
		origin:       options.Origin,
		method:       options.Method,
		path:         options.Path,
		rawQuery:     options.RawQuery,
		headers:      sanitizeHeaders(options.Headers),
		body:         bytes.Clone(options.Body),
		payloadClass: options.PayloadClass,
		connectionID: options.ConnectionID,
		parentID:     options.ParentID,
	}, nil
}

func (request Request) ConnectionID() string { return request.connectionID }
func (request Request) ParentID() string     { return request.parentID }

// PayloadClass reports the frozen proof that this request carries no client
// payload.
func (request Request) PayloadClass() protocolspec.OperationPayloadClass {
	return request.payloadClass
}

func (request Request) RequestID() string {
	return request.requestID
}

func (request Request) Kind() offlinehold.EgressKind {
	return request.kind
}

func (request Request) Origin() originidentity.ClientOrigin {
	return request.origin
}

func (request Request) Method() string {
	return request.method
}

func (request Request) Path() string {
	return request.path
}

func (request Request) RawQuery() string {
	return request.rawQuery
}

func (request Request) Headers() http.Header {
	return request.headers.Clone()
}

func (request Request) Body() []byte {
	return bytes.Clone(request.body)
}

func (request Request) probeTarget() offlinehold.ProbeTarget {
	return offlinehold.ProbeTarget{
		Kind:          request.kind,
		Transport:     offlinehold.ProbeTransportStrictTLS,
		TargetRef:     request.origin.String(),
		NetworkOrigin: request.origin.String(),
		HTTPAuthority: request.origin.HTTPAuthority(),
		TLSServerName: request.origin.Host(),
	}
}

func (request Request) targetURL() *url.URL {
	return &url.URL{
		Scheme:   "https",
		Host:     request.origin.HTTPAuthority(),
		Path:     request.path,
		RawQuery: request.rawQuery,
	}
}

func validateIdentity(label, value string) error {
	if value == "" ||
		len(value) > 1024 ||
		!utf8.ValidString(value) ||
		strings.TrimSpace(value) != value {
		return fmt.Errorf("%s is invalid", label)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("%s contains a control character", label)
		}
	}
	return nil
}

func sanitizeHeaders(source http.Header) http.Header {
	headers := source.Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	for _, token := range strings.Split(headers.Get("Connection"), ",") {
		headers.Del(strings.TrimSpace(token))
	}
	for _, name := range []string{
		"Connection",
		"Proxy-Authorization",
		"Proxy-Connection",
		"Keep-Alive",
		"Te",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade",
		"Forwarded",
		"X-Forwarded-For",
		"X-Forwarded-Host",
		"X-Forwarded-Proto",
		"X-Original-Host",
		"Host",
		"Content-Length",
	} {
		headers.Del(name)
	}
	return headers
}
