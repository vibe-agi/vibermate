package transportprofile

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestCaptureClientHelloReplaysFragmentedRecordsExactly(t *testing.T) {
	t.Parallel()

	handshake := testClientHelloHandshake(
		t,
		"agent.example",
		[]string{"h2", "http/1.1"},
	)
	first := testTLSRecord(handshake[:17])
	second := testTLSRecord(handshake[17:])
	wire := append(bytes.Clone(first), second...)

	client, server := net.Pipe()
	writeDone := make(chan error, 1)
	go func() {
		_, err := client.Write(wire)
		writeDone <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	observation, replay, err := CaptureClientHello(
		ctx,
		server,
		DefaultMaxClientHelloBytes,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
	if !observation.Available() ||
		!slicesEqual(
			observation.OfferedALPN(),
			[]string{"h2", "http/1.1"},
		) {
		t.Fatalf("ClientHello observation = %+v", observation)
	}
	got := make([]byte, len(wire))
	if _, err := io.ReadFull(replay, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, wire) {
		t.Fatal("replayed ClientHello records differ from consumed bytes")
	}
	_ = replay.Close()
	_ = client.Close()
	if err := <-writeDone; err != nil {
		t.Fatalf("write ClientHello: %v", err)
	}
}

func TestCaptureClientHelloRejectsBoundAndCancellation(t *testing.T) {
	t.Parallel()

	t.Run("bound", func(t *testing.T) {
		client, server := net.Pipe()
		defer client.Close()
		defer server.Close()
		payload := make([]byte, 64)
		payload[0] = tlsHandshakeClientHello
		payload[1] = 0
		payload[2] = 1
		payload[3] = 0
		go func() {
			_, _ = client.Write(testTLSRecord(payload))
		}()
		_, _, err := CaptureClientHello(
			context.Background(),
			server,
			32,
		)
		if !errors.Is(err, ErrClientHelloTooLarge) {
			t.Fatalf("CaptureClientHello() error = %v", err)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		client, server := net.Pipe()
		defer client.Close()
		defer server.Close()
		ctx, cancel := context.WithCancelCause(context.Background())
		result := make(chan error, 1)
		go func() {
			_, _, err := CaptureClientHello(
				ctx,
				server,
				DefaultMaxClientHelloBytes,
			)
			result <- err
		}()
		cause := errors.New("capture canceled")
		cancel(cause)
		select {
		case err := <-result:
			if !errors.Is(err, cause) {
				t.Fatalf("CaptureClientHello() error = %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("ClientHello capture did not converge after cancellation")
		}
	})
}

func TestObservationGettersDoNotAlias(t *testing.T) {
	t.Parallel()

	handshake := testClientHelloHandshake(
		t,
		"agent.example",
		[]string{"h2", "http/1.1"},
	)
	observation, err := observeClientHello(
		[]byte{3, 1},
		handshake,
	)
	if err != nil {
		t.Fatal(err)
	}
	alpn := observation.OfferedALPN()
	ciphers := observation.CipherSuites()
	extensions := observation.ExtensionOrder()
	alpn[0] = "mutated"
	ciphers[0] = 0
	extensions[0] = 0xffff
	if observation.OfferedALPN()[0] != "h2" ||
		observation.CipherSuites()[0] != tls.TLS_AES_128_GCM_SHA256 ||
		observation.ExtensionOrder()[0] != 0 {
		t.Fatal("Observation getter returned an alias")
	}
}

func testClientHelloHandshake(
	t *testing.T,
	serverName string,
	alpn []string,
) []byte {
	t.Helper()
	body := make([]byte, 0, 256)
	body = append(body, 3, 3)
	body = append(body, bytes.Repeat([]byte{0x42}, 32)...)
	body = append(body, 0)
	body = append(body, 0, 4)
	body = append(
		body,
		byte(tls.TLS_AES_128_GCM_SHA256>>8),
		byte(tls.TLS_AES_128_GCM_SHA256&0xff),
		byte(tls.TLS_AES_256_GCM_SHA384>>8),
		byte(tls.TLS_AES_256_GCM_SHA384&0xff),
	)
	body = append(body, 1, 0)

	sniName := []byte(serverName)
	sniPayload := make([]byte, 2+1+2+len(sniName))
	binary.BigEndian.PutUint16(sniPayload[:2], uint16(1+2+len(sniName)))
	binary.BigEndian.PutUint16(sniPayload[3:5], uint16(len(sniName)))
	copy(sniPayload[5:], sniName)
	extensions := testExtension(0, sniPayload)

	alpnNames := make([]byte, 0)
	for _, protocol := range alpn {
		if len(protocol) == 0 || len(protocol) > 255 {
			t.Fatalf("invalid test ALPN %q", protocol)
		}
		alpnNames = append(alpnNames, byte(len(protocol)))
		alpnNames = append(alpnNames, protocol...)
	}
	alpnPayload := make([]byte, 2+len(alpnNames))
	binary.BigEndian.PutUint16(alpnPayload[:2], uint16(len(alpnNames)))
	copy(alpnPayload[2:], alpnNames)
	extensions = append(extensions, testExtension(tlsExtensionALPN, alpnPayload)...)
	body = append(body, byte(len(extensions)>>8), byte(len(extensions)))
	body = append(body, extensions...)

	handshake := make([]byte, tlsHandshakeHeaderSize+len(body))
	handshake[0] = tlsHandshakeClientHello
	handshake[1] = byte(len(body) >> 16)
	handshake[2] = byte(len(body) >> 8)
	handshake[3] = byte(len(body))
	copy(handshake[tlsHandshakeHeaderSize:], body)
	return handshake
}

func testExtension(id uint16, payload []byte) []byte {
	extension := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint16(extension[:2], id)
	binary.BigEndian.PutUint16(extension[2:4], uint16(len(payload)))
	copy(extension[4:], payload)
	return extension
}

func testTLSRecord(payload []byte) []byte {
	record := make([]byte, tlsRecordHeaderBytes+len(payload))
	record[0] = tlsRecordHandshake
	record[1] = 3
	record[2] = 1
	binary.BigEndian.PutUint16(record[3:5], uint16(len(payload)))
	copy(record[tlsRecordHeaderBytes:], payload)
	return record
}

func slicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
