package serverhost_test

import (
	"encoding/json"
	"io"
	"net"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/ipallowlist"
	"github.com/vibe-agi/vibermate/internal/serverhost"
)

func TestServerExplicitSOCKSListenerAndShutdown(t *testing.T) {
	options := serverOptions(t, t.TempDir())
	options.Transport = serverhost.TransportOptions{Mode: serverhost.TransportHTTP}
	field := reflect.ValueOf(&options).Elem().FieldByName("SOCKSListenAddress")
	if !field.IsValid() {
		t.Fatal("Server has no explicit SOCKS listener option")
	}
	field.SetString("127.0.0.1:0")
	h, e := serverhost.Start(t.Context(), options)
	if e != nil {
		t.Fatal(e)
	}
	defer shutdownServer(t, h)
	b, _ := json.Marshal(h.Status())
	var status struct {
		SOCKSAddress string `json:"socksAddress"`
	}
	json.Unmarshal(b, &status)
	if status.SOCKSAddress == "" {
		t.Fatal("Server omitted actual SOCKS address")
	}
	c, e := net.DialTimeout("tcp", status.SOCKSAddress, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	c.Write([]byte{5, 1, 0})
	var reply [2]byte
	if _, e = io.ReadFull(c, reply[:]); e != nil || reply != [2]byte{5, 255} {
		t.Fatal("host allowed anonymous SOCKS", reply, e)
	}
	shutdownServer(t, h)
	if next, e := net.DialTimeout("tcp", status.SOCKSAddress, time.Second); e == nil {
		next.Close()
		t.Fatal("Server retained SOCKS listener")
	}
}

func TestServerOccupiedHTTPRollsBackPreboundSOCKSAndRuntime(t *testing.T) {
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	reserved, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	socksAddress := reserved.Addr().String()
	reserved.Close()
	options := serverOptions(t, t.TempDir())
	options.Transport = serverhost.TransportOptions{Mode: serverhost.TransportHTTP}
	options.ListenAddress = occupied.Addr().String()
	options.SOCKSListenAddress = socksAddress
	if host, err := serverhost.Start(t.Context(), options); err == nil {
		shutdownServer(t, host)
		t.Fatal("occupied HTTP startup succeeded")
	}
	probe, err := net.Listen("tcp4", socksAddress)
	if err != nil {
		t.Fatal("failed HTTP startup retained prebound SOCKS", err)
	}
	probe.Close()
	options = serverOptions(t, filepath.Dir(options.Runtime.Paths.DataDirectory()))
	options.Transport = serverhost.TransportOptions{Mode: serverhost.TransportHTTP}
	options.SOCKSListenAddress = socksAddress
	host, err := serverhost.Start(t.Context(), options)
	if err != nil {
		t.Fatal("Runtime guard was not released", err)
	}
	shutdownServer(t, host)
}

func TestServerSOCKSAccessAddressRejectedWithoutListener(t *testing.T) {
	for _, address := range []string{"proxy.example:1080", "0.0.0.0:1080", "[::]:1080", "host:0", "user@host:1080", "host:1080/path"} {
		options := serverOptions(t, t.TempDir())
		options.SOCKSAccessAddress = address
		if address != "proxy.example:1080" {
			options.SOCKSListenAddress = "127.0.0.1:0"
		}
		if host, err := serverhost.Start(t.Context(), options); err == nil {
			shutdownServer(t, host)
			t.Fatalf("invalid SOCKS advertised address %q accepted", address)
		}
	}
}

func TestSOCKSListenerHonorsServerIPAllowlist(t *testing.T) {
	root := t.TempDir()
	options := serverOptions(t, root)
	options.Transport = serverhost.TransportOptions{Mode: serverhost.TransportHTTP}
	options.SOCKSListenAddress = "127.0.0.1:0"
	options.TrustedProxies, _ = ipallowlist.Parse([]string{"127.0.0.1/32"})
	seed, e := serverhost.Start(t.Context(), serverOptions(t, root))
	if e != nil {
		t.Fatal(e)
	}
	shutdownServer(t, seed)
	store, e := ipallowlist.Open(filepath.Join(options.Runtime.Paths.DataDirectory(), "server-admin"), time.Now)
	if e != nil {
		t.Fatal(e)
	}
	list, e := ipallowlist.Parse([]string{"192.0.2.1/32"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = store.Replace(store.Current().Revision, list); e != nil {
		t.Fatal(e)
	}
	h, e := serverhost.Start(t.Context(), options)
	if e != nil {
		t.Fatal(e)
	}
	defer shutdownServer(t, h)
	allowed, e := net.DialTimeout("tcp", h.Status().SOCKSAddress, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	allowed.SetDeadline(time.Now().Add(time.Second))
	allowed.Write(append([]byte("PROXY TCP4 192.0.2.1 127.0.0.1 51000 1080\r\n"), 5, 1, 2))
	var positive [2]byte
	if _, e = io.ReadFull(allowed, positive[:]); e != nil || positive != [2]byte{5, 2} {
		allowed.Close()
		t.Fatal("SOCKS did not apply configured Server proxy header and allowed source", e, positive)
	}
	allowed.Close()
	c, e := net.DialTimeout("tcp", h.Status().SOCKSAddress, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(time.Second))
	c.Write(append([]byte("PROXY TCP4 203.0.113.9 127.0.0.1 51000 1080\r\n"), 5, 1, 2))
	var reply [2]byte
	if _, e = io.ReadFull(c, reply[:]); e == nil {
		t.Fatal("raw SOCKS listener bypassed Server IP allowlist", reply)
	}
}

func TestServerOccupiedSOCKSRollsBackHTTPAndRuntime(t *testing.T) {
	occupied, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer occupied.Close()
	reserved, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	httpAddress := reserved.Addr().String()
	reserved.Close()
	root := t.TempDir()
	options := serverOptions(t, root)
	options.Transport = serverhost.TransportOptions{Mode: serverhost.TransportHTTP}
	options.ListenAddress = httpAddress
	options.SOCKSListenAddress = occupied.Addr().String()
	if h, e := serverhost.Start(t.Context(), options); e == nil {
		shutdownServer(t, h)
		t.Fatal("occupied SOCKS startup succeeded")
	}
	probe, e := net.Listen("tcp4", httpAddress)
	if e != nil {
		t.Fatal("partial SOCKS startup retained HTTP listener", e)
	}
	probe.Close()
	// A fresh coordinator/Store is required for another Runtime incarnation.
	options = serverOptions(t, root)
	options.Transport = serverhost.TransportOptions{Mode: serverhost.TransportHTTP}
	options.SOCKSListenAddress = "127.0.0.1:0"
	h, e := serverhost.Start(t.Context(), options)
	if e != nil {
		t.Fatal(e)
	}
	shutdownServer(t, h)
}
