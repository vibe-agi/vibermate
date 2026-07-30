package transportprofile

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"time"
)

// CaptureClientHello reads complete TLS records until the first ClientHello is
// complete. The returned connection replays every consumed byte exactly once
// before reading from the underlying connection.
func CaptureClientHello(
	ctx context.Context,
	connection net.Conn,
	limit int,
) (Observation, net.Conn, error) {
	if ctx == nil {
		return Observation{}, nil, errors.New(
			"ClientHello capture context is nil",
		)
	}
	if connection == nil {
		return Observation{}, nil, errors.New(
			"ClientHello capture connection is nil",
		)
	}
	if err := validateCaptureLimit(limit); err != nil {
		return Observation{}, nil, err
	}
	if err := ctx.Err(); err != nil {
		return Observation{}, nil, context.Cause(ctx)
	}

	cancelReadDone := make(chan struct{})
	stopCancelRead := context.AfterFunc(ctx, func() {
		_ = connection.SetReadDeadline(time.Now())
		close(cancelReadDone)
	})
	defer func() {
		if !stopCancelRead() {
			<-cancelReadDone
		}
		_ = connection.SetReadDeadline(time.Time{})
	}()
	if deadline, ok := ctx.Deadline(); ok {
		if err := connection.SetReadDeadline(deadline); err != nil {
			return Observation{}, nil, err
		}
	}

	captured := make([]byte, 0, 2048)
	handshake := make([]byte, 0, 2048)
	var recordVersion [2]byte
	expectedHandshakeBytes := -1
	for {
		var header [tlsRecordHeaderBytes]byte
		if _, err := io.ReadFull(connection, header[:]); err != nil {
			return Observation{}, nil, captureReadError(ctx, err)
		}
		if header[0] != tlsRecordHandshake {
			return Observation{}, nil, ErrClientHelloInvalid
		}
		recordBytes := int(binary.BigEndian.Uint16(header[3:5]))
		if recordBytes == 0 ||
			len(captured)+len(header)+recordBytes > limit {
			return Observation{}, nil, ErrClientHelloTooLarge
		}
		payload := make([]byte, recordBytes)
		if _, err := io.ReadFull(connection, payload); err != nil {
			return Observation{}, nil, captureReadError(ctx, err)
		}
		if len(captured) == 0 {
			copy(recordVersion[:], header[1:3])
		}
		captured = append(captured, header[:]...)
		captured = append(captured, payload...)
		handshake = append(handshake, payload...)

		if len(handshake) >= tlsHandshakeHeaderSize &&
			expectedHandshakeBytes < 0 {
			if handshake[0] != tlsHandshakeClientHello {
				return Observation{}, nil, ErrClientHelloInvalid
			}
			expectedHandshakeBytes = tlsHandshakeHeaderSize +
				readUint24(handshake[1:4])
			if expectedHandshakeBytes <= tlsHandshakeHeaderSize ||
				expectedHandshakeBytes > limit-tlsRecordHeaderBytes ||
				expectedHandshakeBytes > 0xffff {
				return Observation{}, nil, ErrClientHelloTooLarge
			}
		}
		if expectedHandshakeBytes < 0 ||
			len(handshake) < expectedHandshakeBytes {
			continue
		}
		observation, err := observeClientHello(
			recordVersion[:],
			handshake[:expectedHandshakeBytes],
		)
		if err != nil {
			return Observation{}, nil, err
		}
		return observation, &replayConnection{
			Conn:   connection,
			replay: bytes.NewReader(captured),
		}, nil
	}
}

func captureReadError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	return err
}

type replayConnection struct {
	net.Conn
	replay *bytes.Reader
}

func (connection *replayConnection) Read(destination []byte) (int, error) {
	if connection.replay != nil && connection.replay.Len() != 0 {
		return connection.replay.Read(destination)
	}
	return connection.Conn.Read(destination)
}
