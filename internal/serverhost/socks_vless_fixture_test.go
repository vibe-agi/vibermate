//go:build darwin || linux

package serverhost_test

import (
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// The existing productruntime fixture pattern is repeated here only for the
// private native server boundary: this package has no access to its _test.go
// helpers. Production routing and guard implementations remain real.
type deliveryNetwork struct {
	directTCP, directUDP                            string
	directDials, directDNS, nodeDNS, dohDNS, denied atomic.Int64
}

func (n *deliveryNetwork) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if (network == "tcp" && address == n.directTCP) || (network == "udp" && address == n.directUDP) {
		n.directDials.Add(1)
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, address)
	}
	n.denied.Add(1)
	return nil, errors.New("owned fixture rejects unexpected Direct target")
}
func (n *deliveryNetwork) LookupNetIP(_ context.Context, network, host string) ([]netip.Addr, error) {
	if network != "ip" {
		return nil, errors.New("unexpected DNS network")
	}
	switch host {
	case "direct.fixture":
		n.directDNS.Add(1)
	case "node.fixture":
		n.nodeDNS.Add(1)
	case "doh.fixture":
		n.dohDNS.Add(1)
	default:
		return nil, errors.New("owned fixture rejects ambient DNS")
	}
	return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
}

type deliveryFixture struct {
	uri                                      string
	tcp, udp, directTCP, directUDP, bad, doh netip.AddrPort
	roots                                    *x509.CertPool
	network                                  *deliveryNetwork
	questions, tcpBytes, udpBytes            atomic.Int64
	pid                                      int
}

func newDeliveryFixture(t *testing.T, ctx context.Context, binaryPath string) *deliveryFixture {
	t.Helper()
	f := &deliveryFixture{}
	f.tcp = deliveryTCPEcho(t, &f.tcpBytes, false)
	f.directTCP = deliveryTCPEcho(t, new(atomic.Int64), false)
	f.udp = deliveryUDPEcho(t, &f.udpBytes)
	f.directUDP = deliveryUDPEcho(t, new(atomic.Int64))
	// This listening owned TCP service deliberately closes without REALITY.
	f.bad = deliveryTCPEcho(t, new(atomic.Int64), true)
	cert, roots := deliveryCertificate(t)
	f.roots = roots
	doh := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, e := io.ReadAll(io.LimitReader(r.Body, 65536))
		if e != nil {
			t.Error(e)
			return
		}
		var p dnsmessage.Parser
		header, e := p.Start(data)
		if e != nil {
			t.Error(e)
			return
		}
		q, e := p.Question()
		if e != nil || q.Name.String() != "proxy.fixture." {
			t.Error("unexpected owned DoH question")
			return
		}
		f.questions.Add(1)
		b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: header.ID, Response: true, RecursionAvailable: true})
		b.StartQuestions()
		b.Question(q)
		b.StartAnswers()
		if q.Type == dnsmessage.TypeA {
			b.AResource(dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 30}, dnsmessage.AResource{A: [4]byte{127, 0, 0, 1}})
		}
		reply, e := b.Finish()
		if e != nil {
			t.Error(e)
			return
		}
		w.Header().Set("Content-Type", "application/dns-message")
		w.Write(reply)
	}))
	doh.Config.ErrorLog = log.New(io.Discard, "", 0)
	doh.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, NextProtos: []string{"h2", "http/1.1"}, CurvePreferences: []tls.CurveID{tls.X25519}}
	doh.StartTLS()
	t.Cleanup(func() { doh.CloseClientConnections(); doh.Close() })
	dohPeer := netip.MustParseAddrPort(doh.Listener.Addr().String())
	f.uri, f.pid = deliveryREALITYServer(t, ctx, binaryPath, dohPeer, []netip.AddrPort{f.tcp, f.udp, dohPeer})
	f.doh = dohPeer
	f.network = &deliveryNetwork{directTCP: f.directTCP.String(), directUDP: f.directUDP.String()}
	t.Logf("owned topology: REALITY pid=%d; TCP=%s UDP=%s directTCP=%s directUDP=%s TLS1.3/DoH=%s", f.pid, f.tcp, f.udp, f.directTCP, f.directUDP, dohPeer)
	return f
}
func deliveryTCPEcho(t *testing.T, count *atomic.Int64, reject bool) netip.AddrPort {
	t.Helper()
	l, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	var mu sync.Mutex
	connections := map[net.Conn]bool{}
	var workers sync.WaitGroup
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, e := l.Accept()
			if e != nil {
				return
			}
			mu.Lock()
			connections[c] = true
			mu.Unlock()
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer func() { c.Close(); mu.Lock(); delete(connections, c); mu.Unlock() }()
				if reject {
					return
				}
				b := make([]byte, 65535)
				for {
					n, e := c.Read(b)
					if n > 0 {
						count.Add(int64(n))
						if _, w := c.Write(b[:n]); w != nil {
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
	t.Cleanup(func() {
		l.Close()
		<-done
		mu.Lock()
		for c := range connections {
			c.Close()
		}
		mu.Unlock()
		workers.Wait()
	})
	return netip.MustParseAddrPort(l.Addr().String())
}
func deliveryUDPEcho(t *testing.T, count *atomic.Int64) netip.AddrPort {
	t.Helper()
	u, e := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		b := make([]byte, 65535)
		for {
			n, a, e := u.ReadFromUDPAddrPort(b)
			if e != nil {
				return
			}
			count.Add(int64(n))
			if _, e = u.WriteToUDPAddrPort(b[:n], a); e != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { u.Close(); <-done })
	return u.LocalAddr().(*net.UDPAddr).AddrPort()
}
func deliveryCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(41), DNSNames: []string{"reality.fixture.invalid", "doh.fixture"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, e := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	parsed, e := x509.ParseCertificate(der)
	if e != nil {
		t.Fatal(e)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, roots
}
func deliveryREALITYServer(t *testing.T, ctx context.Context, binaryPath string, handshake netip.AddrPort, targets []netip.AddrPort) (string, int) {
	t.Helper()
	reserve, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	server := netip.MustParseAddrPort(reserve.Addr().String())
	reserve.Close()
	key, e := ecdh.X25519().GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	identity := make([]byte, 16)
	if _, e = rand.Read(identity); e != nil {
		t.Fatal(e)
	}
	id := hex.EncodeToString(identity)
	uuid := id[:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:]
	sid := id[:16]
	rules := []any{}
	for _, target := range targets {
		rules = append(rules, map[string]any{"type": "field", "ip": []string{target.Addr().String()}, "port": target.Port(), "outboundTag": "owned"})
	}
	config := map[string]any{"log": map[string]any{"loglevel": "none"}, "inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": server.Port(), "protocol": "vless", "settings": map[string]any{"clients": []any{map[string]any{"id": uuid, "flow": "xtls-rprx-vision"}}, "decryption": "none"}, "streamSettings": map[string]any{"network": "raw", "security": "reality", "realitySettings": map[string]any{"target": handshake.String(), "serverNames": []string{"reality.fixture.invalid"}, "privateKey": base64.RawURLEncoding.EncodeToString(key.Bytes()), "shortIds": []string{sid}, "xver": 0}}}}, "outbounds": []any{map[string]any{"tag": "blocked", "protocol": "blackhole"}, map[string]any{"tag": "owned", "protocol": "freedom"}}, "routing": map[string]any{"rules": rules}}
	data, e := json.Marshal(config)
	if e != nil {
		t.Fatal(e)
	}
	defer clear(data)
	path := filepath.Join(t.TempDir(), "owned-reality.json")
	if e = os.WriteFile(path, data, 0600); e != nil {
		t.Fatal(e)
	}
	command := exec.Command(binaryPath, "run", "-c", path)
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if e = command.Start(); e != nil {
		t.Fatal("owned REALITY start failed")
	}
	done := make(chan struct{})
	var exitErr error
	go func() { exitErr = command.Wait(); close(done) }()
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			command.Process.Signal(os.Interrupt)
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				command.Process.Kill()
				<-done
			}
		}
		t.Logf("owned REALITY pid=%d joined", command.Process.Pid)
	})
	startup, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	for {
		select {
		case <-done:
			t.Fatalf("owned REALITY exited before readiness: %v (private native output suppressed)", exitErr)
		default:
		}
		c, e := (&net.Dialer{Timeout: 100 * time.Millisecond}).DialContext(startup, "tcp", server.String())
		if e == nil {
			c.Close()
			break
		}
		select {
		case <-startup.Done():
			t.Fatal("owned REALITY readiness timeout; original process retained for cleanup")
		case <-time.After(20 * time.Millisecond):
		}
	}
	values := url.Values{"security": {"reality"}, "encryption": {"none"}, "type": {"tcp"}, "flow": {"xtls-rprx-vision"}, "sni": {"reality.fixture.invalid"}, "pbk": {base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())}, "sid": {sid}, "fp": {"chrome"}}
	return "vless://" + uuid + "@node.fixture:" + fmt.Sprint(server.Port()) + "?" + values.Encode(), command.Process.Pid
}

type deliveryChild struct {
	pid    int
	config string
}

func deliveryManagedChildren(t *testing.T, binaryPath string, fixturePID int) []deliveryChild {
	t.Helper()
	out, e := exec.Command("ps", "-axo", "pid=,ppid=,command=").Output()
	if e != nil {
		t.Fatal(e)
	}
	children := []deliveryChild{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		pid, _ := strconv.Atoi(fields[0])
		parent, _ := strconv.Atoi(fields[1])
		if parent != os.Getpid() || pid == fixturePID || fields[2] != binaryPath || fields[3] != "run" || fields[4] != "-c" {
			continue
		}
		path := strings.Join(fields[5:], " ")
		info, e := os.Stat(path)
		if e != nil || info.Mode().Perm() != 0600 {
			t.Fatal("managed core private config absent or wrong mode")
		}
		children = append(children, deliveryChild{pid: pid, config: path})
	}
	return children
}
func deliveryPIDAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }
