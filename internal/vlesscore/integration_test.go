package vlesscore

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Removing VLESS routing, packet framing, or lifecycle ownership must break this test.
func TestCoreIntegration(t *testing.T) {
	binary := os.Getenv("VIBERMATE_TEST_XRAY_BINARY")
	if binary == "" {
		t.Skip("real core test requires VIBERMATE_TEST_XRAY_BINARY; no integration executed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	config, tcpTarget, tlsTarget, udpTarget, roots := coreFixture(t, ctx, binary)
	client, err := Start(ctx, config)
	if err != nil {
		t.Fatalf("start adapter: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	conn, err := client.DialContext(ctx, "tcp", tcpTarget.String())
	if err != nil {
		t.Fatalf("TCP dial: %v", err)
	}
	conn.SetDeadline(time.Now().Add(6 * time.Second))
	echo(t, conn, []byte("ViberMate-adapter-TCP\x00\xff\x80\n"))
	conn.Close()
	conn, err = client.DialContext(ctx, "tcp", tlsTarget.String())
	if err != nil {
		t.Fatalf("TLS TCP dial: %v", err)
	}
	conn.SetDeadline(time.Now().Add(6 * time.Second))
	secure := tls.Client(conn, &tls.Config{RootCAs: roots, ServerName: config.ServerName, MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13})
	if err := secure.HandshakeContext(ctx); err != nil {
		t.Fatalf("verified TLS handshake: %v", err)
	}
	echo(t, secure, []byte("ViberMate-adapter-Vision-TLS13\x00\xff\x80"))
	secure.Close()
	packets, err := client.DialUDP(ctx, udpTarget)
	if err != nil {
		t.Fatalf("UDP dial: %v", err)
	}
	for _, payload := range [][]byte{[]byte("ViberMate-adapter-UDP\x00\xff\x80"), bytes.Repeat([]byte{0, 1, 128, 255}, 64), []byte("boundary\x00second-datagram")} {
		if err := packets.WritePacket(ctx, payload); err != nil {
			t.Fatalf("UDP write: %v", err)
		}
		buffer := make([]byte, 8000)
		n, err := packets.ReadPacket(ctx, buffer)
		if err != nil || !bytes.Equal(buffer[:n], payload) {
			t.Fatalf("UDP exact packet echo failed (bytes=%d, error=%v)", n, err)
		}
	}
	if err := packets.WritePacket(ctx, []byte("too-large-for-read")); err != nil {
		t.Fatal(err)
	}
	if _, err := packets.ReadPacket(ctx, make([]byte, 2)); !errors.Is(err, io.ErrShortBuffer) {
		t.Fatalf("short packet buffer: %v", err)
	}
	if err := packets.WritePacket(ctx, nil); err == nil {
		t.Fatal("empty UDP payload accepted")
	}
	if err := packets.WritePacket(ctx, make([]byte, 7527)); err == nil {
		t.Fatal("oversize UDP payload accepted")
	}
	if _, err := client.DialContext(ctx, "tcp", "unresolved.fixture.invalid:80"); err == nil {
		t.Fatal("ambient destination DNS accepted")
	}
	if _, err := client.DialContext(ctx, "udp", tcpTarget.String()); err == nil {
		t.Fatal("stream UDP accepted")
	}
	open, err := client.DialContext(ctx, "tcp", tcpTarget.String())
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	open.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := open.Read(make([]byte, 1)); err == nil {
		t.Fatal("client close did not drain connection")
	}
	if _, err := client.DialUDP(ctx, udpTarget); err == nil {
		t.Fatal("closed client accepted UDP")
	}
	if _, err := packets.ReadPacket(ctx, make([]byte, 100)); err == nil {
		t.Fatal("closed client retained UDP")
	}
	bad := config
	bad.Flow = "unsupported"
	if _, err := Start(ctx, bad); err == nil {
		t.Fatal("unsupported flow accepted")
	}
	t.Log("REALITY+Vision TCP, verified TLS1.3, three exact UDP packets, short-buffer rejection and Close drain verified")
}

// A successful local channel is insufficient: trust must gate delivery to both targets.
func TestTLSCoreIntegration(t *testing.T) {
	binary := os.Getenv("VIBERMATE_TEST_XRAY_BINARY")
	if binary == "" {
		t.Skip("real TLS core requires explicit pinned VIBERMATE_TEST_XRAY_BINARY; no integration executed")
	}
	for _, mode := range []string{"correct-custom-ca", "wrong-ca", "wrong-hostname", "system-uninstalled-ca"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			config, tcpTarget, udpTarget, received := tlsCoreFixture(t, ctx, binary)
			switch mode {
			case "wrong-ca":
				ca, _ := syntheticCertificate(t, "other.fixture.invalid")
				config.TLS = &TLSOptions{Mode: TrustCustomCA, CAPEM: ca}
			case "wrong-hostname":
				config.ServerName = "wrong.fixture.invalid"
			case "system-uninstalled-ca":
				config.TLS = &TLSOptions{Mode: TrustSystem}
			}
			client, err := Start(ctx, config)
			if err != nil {
				t.Fatalf("start TLS adapter: %v", err)
			}
			t.Cleanup(func() {
				client.Close()
				select {
				case <-client.done:
				default:
					t.Error("adapter Wait did not exit")
				}
				if _, err := os.Stat(client.dir); !errors.Is(err, os.ErrNotExist) {
					t.Error("adapter private directory retained")
				}
				t.Logf("adapter pid=%d joined; owned connections closed and private directory removed", client.process.Process.Pid)
			})
			tcpPayload := []byte("ViberMate-outer-TLS-TCP\x00\xff\x80\n")
			conn, dialErr := client.DialContext(ctx, "tcp", tcpTarget.String())
			if mode == "correct-custom-ca" {
				if dialErr != nil {
					t.Fatalf("TLS TCP dial: %v", dialErr)
				}
				conn.SetDeadline(time.Now().Add(3 * time.Second))
				echo(t, conn, tcpPayload)
				conn.Close()
				var got []byte
				for len(got) < len(tcpPayload) {
					select {
					case chunk := <-received:
						got = append(got, chunk...)
					case <-ctx.Done():
						t.Fatal("TCP target observation timed out")
					}
				}
				if !bytes.Equal(got, tcpPayload) {
					t.Fatal("target did not receive exact TCP payload")
				}
				t.Logf("custom CA/original hostname TCP target received %d bytes: %x", len(tcpPayload), tcpPayload)
			} else if dialErr == nil {
				conn.SetDeadline(time.Now().Add(time.Second))
				_, writeErr := conn.Write(tcpPayload)
				n, readErr := conn.Read(make([]byte, len(tcpPayload)))
				conn.Close()
				if writeErr == nil && readErr == nil {
					t.Fatal("untrusted TLS TCP returned payload")
				}
				t.Logf("%s TCP payload failed (read bytes=%d, write error=%v, read error=%v)", mode, n, writeErr, readErr)
			} else {
				t.Logf("%s TCP rejected before payload: %v", mode, dialErr)
			}
			packets, err := client.DialUDP(ctx, udpTarget)
			if err != nil && mode == "correct-custom-ca" {
				t.Fatalf("TLS UDP dial: %v", err)
			}
			if err == nil {
				defer packets.Close()
				for _, payload := range [][]byte{[]byte("ViberMate-outer-TLS-UDP\x00\xff\x80"), bytes.Repeat([]byte{0, 1, 128, 255}, 64), []byte("outer-TLS-boundary\x00second-datagram")} {
					operation, stop := context.WithTimeout(ctx, time.Second)
					writeErr := packets.WritePacket(operation, payload)
					buffer := make([]byte, 8000)
					var n int
					var readErr error
					if writeErr == nil {
						n, readErr = packets.ReadPacket(operation, buffer)
					}
					stop()
					if mode == "correct-custom-ca" {
						if writeErr != nil || readErr != nil || !bytes.Equal(buffer[:n], payload) {
							t.Fatalf("TLS UDP exact echo failed (n=%d, write=%v, read=%v)", n, writeErr, readErr)
						}
						select {
						case got := <-received:
							if !bytes.Equal(got, payload) {
								t.Fatal("target did not receive exact UDP datagram")
							}
						case <-ctx.Done():
							t.Fatal("UDP target observation timed out")
						}
						t.Logf("custom CA/original hostname UDP target received %d bytes: %x", n, payload)
					} else {
						if writeErr == nil && readErr == nil {
							t.Fatal("untrusted TLS UDP returned payload")
						}
						t.Logf("%s UDP payload failed (read bytes=%d, write error=%v, read error=%v)", mode, n, writeErr, readErr)
						break
					}
				}
			} else {
				t.Logf("%s UDP rejected before payload: %v", mode, err)
			}
			if mode != "correct-custom-ca" {
				select {
				case <-received:
					t.Fatal("failed TLS trust delivered payload to owned target")
				case <-time.After(100 * time.Millisecond):
				}
				t.Logf("%s delivered zero TCP/UDP payloads to observed targets", mode)
			}
		})
	}
}

func tlsCoreFixture(t *testing.T, ctx context.Context, binary string, targetLifetime ...time.Duration) (Config, netip.AddrPort, netip.AddrPort, <-chan []byte) {
	t.Helper()
	version, err := exec.CommandContext(ctx, binary, "version").Output()
	if err != nil || !bytes.Contains(version, []byte("26.3.27")) || !bytes.Contains(version, []byte("d2758a0")) {
		t.Fatal("test requires verified Xray 26.3.27/d2758a0")
	}
	cert, key := syntheticCertificate(t, "tls.fixture.invalid")
	tcp, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tcp.Close() })
	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { udp.Close() })
	received := make(chan []byte, 16)
	var workers sync.WaitGroup
	var connections sync.Map
	workers.Add(2)
	go func() {
		defer workers.Done()
		for {
			c, e := tcp.Accept()
			if e != nil {
				return
			}
			connections.Store(c, true)
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer connections.Delete(c)
				defer c.Close()
				c.SetDeadline(time.Now().Add(coreFixtureLifetime(10*time.Second, targetLifetime)))
				b := make([]byte, 8000)
				for {
					n, e := c.Read(b)
					if n > 0 {
						received <- append([]byte(nil), b[:n]...)
						if _, err := c.Write(b[:n]); err != nil {
							return
						}
					}
					if e != nil {
						return
					}
				}
			}()
		}
	}()
	go func() {
		defer workers.Done()
		b := make([]byte, 65535)
		for {
			n, p, e := udp.ReadFromUDP(b)
			if e != nil {
				return
			}
			received <- append([]byte(nil), b[:n]...)
			if _, e := udp.WriteToUDP(b[:n], p); e != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		tcp.Close()
		udp.Close()
		connections.Range(func(k, v any) bool { k.(net.Conn).Close(); return true })
		workers.Wait()
		t.Log("TLS fixture TCP/UDP listeners, accepted connections and target workers joined")
	})
	reservation, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := netip.MustParseAddrPort(reservation.Addr().String())
	reservation.Close()
	id, err := randomUUID()
	if err != nil {
		t.Fatal(err)
	}
	hexID := hex.EncodeToString(id[:])
	uuid := hexID[:8] + "-" + hexID[8:12] + "-" + hexID[12:16] + "-" + hexID[16:20] + "-" + hexID[20:]
	config := Config{Executable: binary, Server: server, UUID: uuid, ServerName: "tls.fixture.invalid", Security: SecurityTLS, TLS: &TLSOptions{Mode: TrustCustomCA, CAPEM: cert}}
	tcpTarget := netip.MustParseAddrPort(tcp.Addr().String())
	udpTarget := udp.LocalAddr().(*net.UDPAddr).AddrPort()
	serverConfig := map[string]any{"log": map[string]any{"loglevel": "none"}, "inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": server.Port(), "protocol": "vless", "settings": map[string]any{"clients": []any{map[string]any{"id": uuid}}, "decryption": "none"}, "streamSettings": map[string]any{"network": "raw", "security": "tls", "tlsSettings": map[string]any{"certificates": []any{map[string]any{"certificate": strings.Split(strings.TrimSpace(cert), "\n"), "key": strings.Split(strings.TrimSpace(key), "\n")}}}}}}, "outbounds": []any{map[string]any{"tag": "blocked", "protocol": "blackhole"}, map[string]any{"tag": "fixtures", "protocol": "freedom"}}, "routing": map[string]any{"rules": []any{map[string]any{"type": "field", "ip": []string{"127.0.0.1"}, "port": tcpTarget.Port(), "outboundTag": "fixtures"}, map[string]any{"type": "field", "ip": []string{"127.0.0.1"}, "port": udpTarget.Port(), "outboundTag": "fixtures"}}}}
	encoded, err := json.Marshal(serverConfig)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "tls-server.json")
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "run", "-c", path)
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Start(); err != nil {
		t.Fatal("TLS fixture process start failed")
	}
	done := make(chan struct{})
	var waitErr error
	go func() { waitErr = command.Wait(); close(done) }()
	t.Cleanup(func() {
		command.Process.Signal(os.Interrupt)
		select {
		case <-done:
			t.Logf("TLS fixture pid=%d Wait exited: %v", command.Process.Pid, waitErr)
		case <-time.After(3 * time.Second):
			command.Process.Kill()
			<-done
			t.Logf("TLS fixture pid=%d killed and Wait exited: %v", command.Process.Pid, waitErr)
		}
	})
	for {
		select {
		case <-done:
			t.Fatal("TLS fixture exited before readiness")
		default:
		}
		c, e := (&net.Dialer{Timeout: 100 * time.Millisecond}).DialContext(ctx, "tcp", server.String())
		if e == nil {
			c.Close()
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("TLS fixture readiness timed out")
		case <-time.After(20 * time.Millisecond):
		}
	}
	return config, tcpTarget, udpTarget, received
}

func echo(t *testing.T, conn net.Conn, payload []byte) {
	t.Helper()
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	result := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, result); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(result, payload) {
		t.Fatal("TCP exact echo mismatch")
	}
}

func coreFixture(t *testing.T, ctx context.Context, binary string, targetLifetime ...time.Duration) (Config, netip.AddrPort, netip.AddrPort, netip.AddrPort, *x509.CertPool) {
	return coreFixtureObserved(t, ctx, binary, nil, targetLifetime...)
}

func coreFixtureObserved(t *testing.T, ctx context.Context, binary string, observeUDP func([]byte), targetLifetime ...time.Duration) (Config, netip.AddrPort, netip.AddrPort, netip.AddrPort, *x509.CertPool) {
	t.Helper()
	version, err := exec.CommandContext(ctx, binary, "version").Output()
	if err != nil || !bytes.Contains(version, []byte("26.3.27")) || !bytes.Contains(version, []byte("d2758a0")) {
		t.Fatal("test requires verified Xray 26.3.27/d2758a0 binary")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "reality.fixture.invalid"}, DNSNames: []string{"reality.fixture.invalid"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	tcp, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	secure, err := tls.Listen("tcp4", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, NextProtos: []string{"h2", "http/1.1"}, CurvePreferences: []tls.CurveID{tls.X25519}})
	if err != nil {
		t.Fatal(err)
	}
	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	var connections sync.Map
	serve := func(l net.Listener) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				c, e := l.Accept()
				if e != nil {
					return
				}
				connections.Store(c, true)
				workers.Add(1)
				go func() {
					defer workers.Done()
					defer connections.Delete(c)
					defer c.Close()
					c.SetDeadline(time.Now().Add(coreFixtureLifetime(15*time.Second, targetLifetime)))
					io.Copy(c, c)
				}()
			}
		}()
	}
	serve(tcp)
	serve(secure)
	workers.Add(1)
	go func() {
		defer workers.Done()
		b := make([]byte, 65535)
		for {
			n, p, e := udp.ReadFromUDP(b)
			if e != nil {
				return
			}
			if observeUDP != nil {
				observeUDP(b[:n])
			}
			if _, e = udp.WriteToUDP(b[:n], p); e != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		tcp.Close()
		secure.Close()
		udp.Close()
		connections.Range(func(k, v any) bool { k.(net.Conn).Close(); return true })
		workers.Wait()
	})
	portListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := netip.MustParseAddrPort(portListener.Addr().String())
	portListener.Close()
	realityKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	uuid := hex.EncodeToString(random[:4]) + "-" + hex.EncodeToString(random[4:6]) + "-" + hex.EncodeToString(random[6:8]) + "-" + hex.EncodeToString(random[8:10]) + "-" + hex.EncodeToString(random[10:])
	shortID := hex.EncodeToString(random[:8])
	config := Config{Executable: binary, Server: server, UUID: uuid, ServerName: "reality.fixture.invalid", Password: base64.RawURLEncoding.EncodeToString(realityKey.PublicKey().Bytes()), ShortID: shortID}
	serverConfig := map[string]any{"log": map[string]any{"loglevel": "none"}, "inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": server.Port(), "protocol": "vless", "settings": map[string]any{"clients": []any{map[string]any{"id": uuid, "flow": "xtls-rprx-vision"}}, "decryption": "none"}, "streamSettings": map[string]any{"network": "raw", "security": "reality", "realitySettings": map[string]any{"show": false, "target": secure.Addr().String(), "serverNames": []string{config.ServerName}, "privateKey": base64.RawURLEncoding.EncodeToString(realityKey.Bytes()), "shortIds": []string{shortID}, "xver": 0}}}}, "outbounds": []any{map[string]any{"tag": "blocked", "protocol": "blackhole"}, map[string]any{"tag": "fixtures", "protocol": "freedom"}}, "routing": map[string]any{"rules": []any{map[string]any{"type": "field", "ip": []string{"127.0.0.1"}, "port": netip.MustParseAddrPort(tcp.Addr().String()).Port(), "outboundTag": "fixtures"}, map[string]any{"type": "field", "ip": []string{"127.0.0.1"}, "port": netip.MustParseAddrPort(secure.Addr().String()).Port(), "outboundTag": "fixtures"}, map[string]any{"type": "field", "ip": []string{"127.0.0.1"}, "port": udp.LocalAddr().(*net.UDPAddr).Port, "outboundTag": "fixtures"}}}}
	encoded, err := json.Marshal(serverConfig)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "server.json")
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "run", "-c", path)
	if err := command.Start(); err != nil {
		t.Fatal("fixture core start failed")
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	t.Cleanup(func() {
		command.Process.Signal(os.Interrupt)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			command.Process.Kill()
			<-done
		}
	})
	for {
		select {
		case <-done:
			t.Fatal("fixture core exited before readiness")
		default:
		}
		c, e := (&net.Dialer{Timeout: 100 * time.Millisecond}).DialContext(ctx, "tcp", server.String())
		if e == nil {
			c.Close()
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("fixture readiness timed out")
		case <-time.After(20 * time.Millisecond):
		}
	}
	return config, netip.MustParseAddrPort(tcp.Addr().String()), netip.MustParseAddrPort(secure.Addr().String()), udp.LocalAddr().(*net.UDPAddr).AddrPort(), roots
}

// Long-lived setup qualification must outlive the existing short echo fixtures.
func coreFixtureLifetime(fallback time.Duration, selected []time.Duration) time.Duration {
	if len(selected) != 0 {
		return selected[0]
	}
	return fallback
}
