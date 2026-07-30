// Package transportprofile captures a bounded downstream TLS ClientHello and
// applies an immutable Access transport profile to one upstream connection.
//
// It preserves protocol conversion as a separate codec/IR concern. This
// package handles only transport identity, strict TLS, and transport evidence.
package transportprofile

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
)

const (
	DefaultMaxClientHelloBytes = 64 << 10

	tlsRecordHeaderBytes    = 5
	tlsHandshakeHeaderSize  = 4
	tlsRecordHandshake      = 22
	tlsHandshakeClientHello = 1
	tlsExtensionALPN        = 16
)

var (
	ErrClientHelloUnavailable = errors.New("client TLS ClientHello is unavailable")
	ErrClientHelloInvalid     = errors.New("client TLS ClientHello is invalid")
	ErrClientHelloTooLarge    = errors.New("client TLS ClientHello exceeds the configured bound")
)

// Observation is an immutable, request-scoped view of the downstream
// ClientHello. It intentionally exposes no JA3-style identity claim.
type Observation struct {
	valid                    bool
	fingerprintRecord        []byte
	offeredALPN              []string
	cipherSuites             []uint16
	extensionOrder           []uint16
	downstreamNegotiatedALPN string
}

func (observation Observation) Available() bool {
	return observation.valid
}

func (observation Observation) OfferedALPN() []string {
	return slices.Clone(observation.offeredALPN)
}

func (observation Observation) CipherSuites() []uint16 {
	return slices.Clone(observation.cipherSuites)
}

func (observation Observation) ExtensionOrder() []uint16 {
	return slices.Clone(observation.extensionOrder)
}

func (observation Observation) DownstreamNegotiatedALPN() string {
	return observation.downstreamNegotiatedALPN
}

// WithDownstreamNegotiatedALPN returns a private copy carrying the protocol
// selected by the local TLS server after the captured bytes were replayed.
func (observation Observation) WithDownstreamNegotiatedALPN(
	protocol string,
) (Observation, error) {
	if !observation.valid {
		return Observation{}, ErrClientHelloUnavailable
	}
	if protocol != "" && !slices.Contains(observation.offeredALPN, protocol) {
		return Observation{}, errors.New(
			"downstream negotiated ALPN was not offered by the client",
		)
	}
	cloned := observation.clone()
	cloned.downstreamNegotiatedALPN = protocol
	return cloned, nil
}

func (observation Observation) clone() Observation {
	cloned := observation
	cloned.fingerprintRecord = bytes.Clone(observation.fingerprintRecord)
	cloned.offeredALPN = slices.Clone(observation.offeredALPN)
	cloned.cipherSuites = slices.Clone(observation.cipherSuites)
	cloned.extensionOrder = slices.Clone(observation.extensionOrder)
	return cloned
}

func observeClientHello(
	recordVersion []byte,
	handshake []byte,
) (Observation, error) {
	if len(recordVersion) != 2 ||
		len(handshake) < tlsHandshakeHeaderSize ||
		handshake[0] != tlsHandshakeClientHello {
		return Observation{}, ErrClientHelloInvalid
	}
	bodyBytes := readUint24(handshake[1:4])
	if bodyBytes <= 0 ||
		bodyBytes != len(handshake)-tlsHandshakeHeaderSize ||
		len(handshake) > 0xffff {
		return Observation{}, ErrClientHelloInvalid
	}
	body := handshake[tlsHandshakeHeaderSize:]
	cipherSuites, extensions, offeredALPN, err := parseClientHelloBody(body)
	if err != nil {
		return Observation{}, errors.Join(ErrClientHelloInvalid, err)
	}
	record := make([]byte, tlsRecordHeaderBytes+len(handshake))
	record[0] = tlsRecordHandshake
	copy(record[1:3], recordVersion)
	binary.BigEndian.PutUint16(record[3:5], uint16(len(handshake)))
	copy(record[tlsRecordHeaderBytes:], handshake)
	return Observation{
		valid:             true,
		fingerprintRecord: record,
		offeredALPN:       offeredALPN,
		cipherSuites:      cipherSuites,
		extensionOrder:    extensions,
	}, nil
}

func parseClientHelloBody(
	body []byte,
) ([]uint16, []uint16, []string, error) {
	cursor := byteCursor{data: body}
	if !cursor.skip(2 + 32) {
		return nil, nil, nil, errors.New("ClientHello fixed fields are truncated")
	}
	sessionID, ok := cursor.vector8()
	if !ok || len(sessionID) > 32 {
		return nil, nil, nil, errors.New("ClientHello session ID is invalid")
	}
	cipherBytes, ok := cursor.vector16()
	if !ok || len(cipherBytes) == 0 || len(cipherBytes)%2 != 0 {
		return nil, nil, nil, errors.New("ClientHello cipher suites are invalid")
	}
	cipherSuites := make([]uint16, 0, len(cipherBytes)/2)
	for len(cipherBytes) != 0 {
		cipherSuites = append(
			cipherSuites,
			binary.BigEndian.Uint16(cipherBytes[:2]),
		)
		cipherBytes = cipherBytes[2:]
	}
	compressionMethods, ok := cursor.vector8()
	if !ok || len(compressionMethods) == 0 {
		return nil, nil, nil, errors.New(
			"ClientHello compression methods are invalid",
		)
	}
	if cursor.remaining() == 0 {
		return cipherSuites, nil, nil, nil
	}
	extensionBytes, ok := cursor.vector16()
	if !ok || cursor.remaining() != 0 {
		return nil, nil, nil, errors.New("ClientHello extensions are invalid")
	}

	extensionCursor := byteCursor{data: extensionBytes}
	extensionOrder := make([]uint16, 0)
	var offeredALPN []string
	alpnSeen := false
	for extensionCursor.remaining() != 0 {
		extensionID, ok := extensionCursor.uint16()
		if !ok {
			return nil, nil, nil, errors.New(
				"ClientHello extension identifier is truncated",
			)
		}
		payload, ok := extensionCursor.vector16()
		if !ok {
			return nil, nil, nil, errors.New(
				"ClientHello extension payload is truncated",
			)
		}
		extensionOrder = append(extensionOrder, extensionID)
		if extensionID != tlsExtensionALPN {
			continue
		}
		if alpnSeen {
			return nil, nil, nil, errors.New(
				"ClientHello contains duplicate ALPN extensions",
			)
		}
		alpnSeen = true
		offeredALPN, ok = parseALPN(payload)
		if !ok {
			return nil, nil, nil, errors.New(
				"ClientHello ALPN extension is invalid",
			)
		}
	}
	return cipherSuites, extensionOrder, offeredALPN, nil
}

func parseALPN(payload []byte) ([]string, bool) {
	cursor := byteCursor{data: payload}
	protocolBytes, ok := cursor.vector16()
	if !ok || len(protocolBytes) == 0 || cursor.remaining() != 0 {
		return nil, false
	}
	protocolCursor := byteCursor{data: protocolBytes}
	protocols := make([]string, 0)
	for protocolCursor.remaining() != 0 {
		protocol, ok := protocolCursor.vector8()
		if !ok || len(protocol) == 0 {
			return nil, false
		}
		value := string(protocol)
		if slices.Contains(protocols, value) {
			return nil, false
		}
		protocols = append(protocols, value)
	}
	return protocols, true
}

type byteCursor struct {
	data   []byte
	offset int
}

func (cursor *byteCursor) remaining() int {
	return len(cursor.data) - cursor.offset
}

func (cursor *byteCursor) skip(count int) bool {
	if count < 0 || count > cursor.remaining() {
		return false
	}
	cursor.offset += count
	return true
}

func (cursor *byteCursor) uint16() (uint16, bool) {
	if cursor.remaining() < 2 {
		return 0, false
	}
	value := binary.BigEndian.Uint16(cursor.data[cursor.offset : cursor.offset+2])
	cursor.offset += 2
	return value, true
}

func (cursor *byteCursor) vector8() ([]byte, bool) {
	if cursor.remaining() < 1 {
		return nil, false
	}
	length := int(cursor.data[cursor.offset])
	cursor.offset++
	if length > cursor.remaining() {
		return nil, false
	}
	value := cursor.data[cursor.offset : cursor.offset+length]
	cursor.offset += length
	return value, true
}

func (cursor *byteCursor) vector16() ([]byte, bool) {
	length, ok := cursor.uint16()
	if !ok || int(length) > cursor.remaining() {
		return nil, false
	}
	value := cursor.data[cursor.offset : cursor.offset+int(length)]
	cursor.offset += int(length)
	return value, true
}

func readUint24(value []byte) int {
	if len(value) != 3 {
		return -1
	}
	return int(value[0])<<16 | int(value[1])<<8 | int(value[2])
}

func validateCaptureLimit(limit int) error {
	if limit < tlsRecordHeaderBytes+tlsHandshakeHeaderSize ||
		limit > DefaultMaxClientHelloBytes {
		return fmt.Errorf(
			"ClientHello capture limit must be between %d and %d bytes",
			tlsRecordHeaderBytes+tlsHandshakeHeaderSize,
			DefaultMaxClientHelloBytes,
		)
	}
	return nil
}
