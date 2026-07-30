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

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/providertransport"
)

type RequestOptions struct {
	RequestID string
	Kind      offlinehold.EgressKind
	Origin    access.ClientOrigin
	Method    string
	Path      string
	RawQuery  string
	Headers   http.Header
	Body      []byte
}

// Request freezes an original-origin auxiliary or opaque representation. It
// cannot replace its ClientOrigin with a provider target or arbitrary URL.
type Request struct {
	requestID string
	kind      offlinehold.EgressKind
	origin    access.ClientOrigin
	method    string
	path      string
	rawQuery  string
	headers   http.Header
	body      []byte
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
	if options.Origin.String() == "" ||
		options.Origin.HTTPAuthority() == "" ||
		options.Origin.TLSServerName() == "" {
		return Request{}, errors.New("original ClientOrigin is incomplete")
	}
	if options.Method == "" ||
		strings.ToUpper(options.Method) != options.Method ||
		options.Path == "" ||
		options.Path[0] != '/' ||
		strings.ContainsAny(options.Path, "%?#\\\r\n") ||
		strings.ContainsAny(options.RawQuery, "#\r\n") ||
		len(options.Body) > providertransport.MaxProviderRequestBytes {
		return Request{}, errors.New("original-origin request target is invalid")
	}
	return Request{
		requestID: options.RequestID,
		kind:      options.Kind,
		origin:    options.Origin,
		method:    options.Method,
		path:      options.Path,
		rawQuery:  options.RawQuery,
		headers:   sanitizeHeaders(options.Headers),
		body:      bytes.Clone(options.Body),
	}, nil
}

func (request Request) RequestID() string {
	return request.requestID
}

func (request Request) Kind() offlinehold.EgressKind {
	return request.kind
}

func (request Request) Origin() access.ClientOrigin {
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
		TargetRef:     request.origin.String(),
		NetworkOrigin: request.origin.String(),
		HTTPAuthority: request.origin.HTTPAuthority(),
		TLSServerName: request.origin.TLSServerName(),
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
