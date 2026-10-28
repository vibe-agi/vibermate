package flowbroker

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
)

const maxRequestBytes = 512

// TokenEnvironment names the variable a run sets, to its registration's
// token, in its Agent's environment. Descendants inherit it, which attributes
// a process orphaned before its first connection (ADR 0024).
const TokenEnvironment = "VIBERMATE_TRANSPARENT_CAPTURE"

// ValidToken reports whether token can name a registration: 32 to 128
// URL-safe base64 characters.
func ValidToken(token string) bool {
	if len(token) < 32 || len(token) > 128 {
		return false
	}
	for i := 0; i < len(token); i++ {
		c := token[i]
		if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

type flowRequest struct {
	Version int    `json:"v"`
	PID     int    `json:"pid"`
	Secret  string `json:"secret,omitempty"`
	// Hello announces that the extension is active; it carries no flow.
	Hello bool `json:"hello,omitempty"`
	// Stream keeps a hello connection open for the extension's lifetime, so
	// the broker learns of a crash or stop the moment the socket closes.
	Stream bool `json:"stream,omitempty"`
	// QueryFailures is the extension's running count of ownership queries
	// it could not complete, reported with each hello.
	QueryFailures uint64 `json:"queryFailures,omitempty"`
	// Destination is a TCP flow's remote address and port (ADR 0025), so the
	// broker can send a run's scoped destinations direct. Optional.
	Destination string `json:"dst,omitempty"`
}

func readFlowRequest(r io.Reader) (flowRequest, error) {
	var size [2]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return flowRequest{}, err
	}
	n := int(binary.BigEndian.Uint16(size[:]))
	if n == 0 || n > maxRequestBytes {
		return flowRequest{}, errors.New("flow request is invalid")
	}
	encoded := make([]byte, n)
	if _, err := io.ReadFull(r, encoded); err != nil {
		return flowRequest{}, err
	}
	var request flowRequest
	if err := json.Unmarshal(encoded, &request); err != nil || request.Version != 1 || (request.Hello != (request.PID == 0)) || (!request.Hello && request.PID <= 1) ||
		(!request.Hello && (request.Stream || request.QueryFailures != 0)) ||
		(request.Destination != "" && (request.Hello || !validDestination(request.Destination))) {
		return flowRequest{}, errors.New("flow request is invalid")
	}
	return request, nil
}

// WriteHello frames the extension's periodic liveness announcement.
func WriteHello(w io.Writer, secret string) error {
	return writeRequest(w, flowRequest{Version: 1, Secret: secret, Hello: true})
}

// WriteHelloStream frames the hello that opens the extension's liveness
// stream; the connection then stays open until the extension stops.
func WriteHelloStream(w io.Writer, secret string, queryFailures uint64) error {
	return writeRequest(w, flowRequest{Version: 1, Secret: secret, Hello: true, Stream: true, QueryFailures: queryFailures})
}

// WriteFlowRequest frames a flow ownership query, as the extension sends it.
// secret is empty on a Unix socket, where peer credentials authenticate.
func WriteFlowRequest(w io.Writer, pid int, secret string) error {
	return writeRequest(w, flowRequest{Version: 1, PID: pid, Secret: secret})
}

// WriteFlowRequestTo frames an ownership query that names the flow's
// destination, as a capture-scope-aware extension sends it.
func WriteFlowRequestTo(w io.Writer, pid int, secret string, destination netip.AddrPort) error {
	return writeRequest(w, flowRequest{Version: 1, PID: pid, Secret: secret, Destination: destination.String()})
}

func validDestination(value string) bool {
	destination, err := netip.ParseAddrPort(value)
	return err == nil && destination.Port() != 0
}

func writeRequest(w io.Writer, request flowRequest) error {
	encoded, err := json.Marshal(request)
	if err != nil {
		return err
	}
	frame := make([]byte, 2+len(encoded))
	binary.BigEndian.PutUint16(frame, uint16(len(encoded)))
	copy(frame[2:], encoded)
	_, err = w.Write(frame)
	return err
}

// BypassAddress is one address rule of a run's capture scope, sent with its
// registration (ADR 0025).
type BypassAddress struct {
	Prefix string `json:"prefix"`
	Port   uint16 `json:"port,omitempty"`
}
