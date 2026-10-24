// Package flowingress turns one intercepted TCP flow into a CONNECT through a
// Capture Run's existing proxy frontier. The platform capture layer (macOS
// transparent proxy, Linux namespace netstack) sends a small header naming the
// original destination, then the client's bytes. Ownership is never in the
// header: an ingress belongs to exactly one run.
package flowingress

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"strings"
)

// MaxHeaderBytes bounds the JSON header that precedes a flow.
const MaxHeaderBytes = 1024

// Header describes the flow's original destination.
type Header struct {
	Version     int            `json:"v"`
	Protocol    string         `json:"proto"`
	Destination netip.AddrPort `json:"dst"`
	// Host is a name the platform reported for the destination. It is never
	// a guess; when absent, the ingress names the destination itself.
	Host string `json:"host,omitempty"`
}

var errInvalidHeader = errors.New("flow header is invalid")

// WriteHeader frames header as a 2-byte big-endian length and JSON.
func WriteHeader(w io.Writer, header Header) error {
	if err := header.validate(); err != nil {
		return err
	}
	encoded, err := json.Marshal(header)
	if err != nil {
		return err
	}
	if len(encoded) > MaxHeaderBytes {
		return errInvalidHeader
	}
	frame := make([]byte, 2+len(encoded))
	binary.BigEndian.PutUint16(frame, uint16(len(encoded)))
	copy(frame[2:], encoded)
	_, err = w.Write(frame)
	return err
}

// ReadHeader reads one framed header and rejects anything it does not model.
func ReadHeader(r io.Reader) (Header, error) {
	var size [2]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return Header{}, err
	}
	n := int(binary.BigEndian.Uint16(size[:]))
	if n == 0 || n > MaxHeaderBytes {
		return Header{}, errInvalidHeader
	}
	encoded := make([]byte, n)
	if _, err := io.ReadFull(r, encoded); err != nil {
		return Header{}, err
	}
	var header Header
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&header); err != nil || decoder.More() {
		return Header{}, errInvalidHeader
	}
	if err := header.validate(); err != nil {
		return Header{}, err
	}
	return header, nil
}

func (header Header) validate() error {
	if header.Version != 1 || header.Protocol != "tcp" ||
		!header.Destination.IsValid() || header.Destination.Port() == 0 {
		return errInvalidHeader
	}
	if header.Host != "" && !validHostName(header.Host) {
		return fmt.Errorf("%w: host name", errInvalidHeader)
	}
	return nil
}

// validHostName accepts a lower-case ASCII DNS name: what the Runtime's
// policy compares against. IP literals belong in Destination instead.
func validHostName(host string) bool {
	if len(host) == 0 || len(host) > 253 || strings.HasSuffix(host, ".") {
		return false
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 ||
			label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' {
				return false
			}
		}
	}
	return true
}
