package flowbroker

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
)

const maxRequestBytes = 512

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
		(!request.Hello && (request.Stream || request.QueryFailures != 0)) {
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
