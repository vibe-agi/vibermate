package vlesscore

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// Enabling the bare core UDP listener lets unrelated loopback senders through.
func TestPrivateUDPRejectsUnassociatedSender(t *testing.T) {
	binary := os.Getenv("VIBERMATE_TEST_XRAY_BINARY")
	if binary == "" {
		t.Skip("requires explicit verified core; no integration executed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	config, _, _, target, _ := coreFixture(t, ctx, binary)
	client, err := Start(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	legitimate, err := client.DialUDP(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("legitimate-association")
	if err := legitimate.WritePacket(ctx, payload); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 100)
	if n, err := legitimate.ReadPacket(ctx, buffer); err != nil || !bytes.Equal(buffer[:n], payload) {
		t.Fatal("legitimate packet failed before unauthorized probe")
	}
	legitimate.Close()
	relay, err := net.ResolveUDPAddr("udp4", client.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	attacker, err := net.DialUDP("udp4", nil, relay)
	if err != nil {
		t.Fatal(err)
	}
	defer attacker.Close()
	attacker.SetDeadline(time.Now().Add(600 * time.Millisecond))
	unauthorized := []byte("unassociated-loopback-packet")
	packet := append([]byte{0, 0, 0}, encodeAddress(target)...)
	packet = append(packet, unauthorized...)
	if _, err := attacker.Write(packet); err != nil {
		t.Fatal("unauthorized probe could not send")
	}
	if n, err := attacker.Read(buffer); err == nil && bytes.Contains(buffer[:n], unauthorized) {
		t.Fatal("unauthorized loopback UDP sender reached echo after legitimate control closed")
	}
	t.Log("separate loopback UDP sender received no relayed packet")
}

func TestClientPointerFormattingRedactsCredentials(t *testing.T) {
	client := &Client{username: "private-user-sentinel", password: "private-password-sentinel"}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		printed := fmt.Sprintf(format, client)
		if strings.Contains(printed, client.username) || strings.Contains(printed, client.password) {
			t.Fatalf("client pointer formatting leaked credentials for format %s", format)
		}
	}
}

func TestPrivateUDPRejectsWrongUUID(t *testing.T) {
	binaryPath := os.Getenv("VIBERMATE_TEST_XRAY_BINARY")
	if binaryPath == "" {
		t.Skip("requires explicit verified core; no integration executed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	received := make(chan []byte, 4)
	config, _, _, target, _ := coreFixtureObserved(t, ctx, binaryPath, func(b []byte) { received <- append([]byte(nil), b...) })
	client, err := Start(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	stream, err := net.Dial("unix", client.udpPath)
	if err != nil {
		t.Fatal("private Unix connection failed")
	}
	defer stream.Close()
	stream.SetDeadline(time.Now().Add(time.Second))
	wrong := client.udpID
	wrong[0] ^= 255
	// Independently encode VLESS v0, wrong UUID, no addons, UDP, port then IPv4.
	header := append([]byte{0}, wrong[:]...)
	header = append(header, 0, 2)
	header = append(header, byte(target.Port()>>8), byte(target.Port()), 1)
	ip := target.Addr().As4()
	header = append(header, ip[:]...)
	payload := []byte("wrong-identity-must-not-reach-echo")
	header = binary.BigEndian.AppendUint16(header, uint16(len(payload)))
	header = append(header, payload...)
	if err := writeAll(stream, header); err != nil {
		t.Fatal("wrong-identity probe could not send")
	}
	if _, err := stream.Read(make([]byte, 100)); err == nil {
		t.Fatal("wrong UUID received a VLESS response")
	}
	select {
	case <-received:
		t.Fatal("wrong UUID reached UDP fixture")
	case <-time.After(600 * time.Millisecond):
	}
	t.Log("wrong internal UUID was rejected and no packet reached the owned echo")
}
