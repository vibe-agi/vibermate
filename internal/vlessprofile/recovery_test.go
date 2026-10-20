package vlessprofile

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/secretstore"
)

// A completed core must not permanently poison its immutable policy's cache.
// Each Dial below is a new operation; no application request is retried.
func TestExitedCoreRecoversOnLaterDialWithFreshBootstrap(t *testing.T) {
	manager, policy, resolver := recoveryManager(t, nil)
	first := recoveryDial(t, manager, policy)
	firstID := recoveryIdentity(t, first)
	if firstID.server != "192.0.2.10" {
		t.Fatalf("initial bootstrap = %q", firstID.server)
	}
	if _, err := io.WriteString(first, "exit\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Read(make([]byte, 1)); err == nil {
		t.Fatal("exited core retained the first connection")
	}
	first.Close()
	resolver.address.Store("192.0.2.20")
	// EOF can precede the process wait goroutine by a scheduling turn. Wait
	// for recovery using independent new dials, bounded by this test's context.
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	type dialResult struct {
		conn net.Conn
		err  error
	}
	results := make(chan dialResult, 8)
	for range 8 {
		go func() {
			var result dialResult
			for result.conn == nil && ctx.Err() == nil {
				result.conn, result.err = manager.DialContext(ctx, policy, "tcp", "203.0.113.10:443")
				if result.err != nil {
					select {
					case <-time.After(time.Millisecond):
					case <-ctx.Done():
					}
				}
			}
			results <- result
		}()
	}
	var next net.Conn
	var recovered recoveryPeer
	for range 8 {
		result := awaitLifecycle(t, results, "concurrent later dial")
		if result.conn == nil {
			t.Errorf("later dial kept reusing an exited core: %v", result.err)
			continue
		}
		defer result.conn.Close()
		id := recoveryIdentity(t, result.conn)
		if id.pid == firstID.pid || id.server != "192.0.2.20" || recovered.pid != 0 && id != recovered {
			t.Errorf("later dials did not share one fresh core: before=%+v previous=%+v after=%+v", firstID, recovered, id)
		}
		next, recovered = result.conn, id
	}
	if next == nil {
		t.Fatal("no later dial recovered")
	}
	if got := resolver.reads.Load(); got != 2 {
		t.Fatalf("bootstrap reads = %d, want one per successful core", got)
	}
	if _, err := io.WriteString(next, "echo:after-recovery\n"); err != nil {
		t.Fatal(err)
	}
	if got, err := bufio.NewReader(next).ReadString('\n'); err != nil || got != "echo:after-recovery\n" {
		t.Fatalf("fresh connection did not carry exact bytes: %q, %v", got, err)
	}
}

func TestLiveCoreSurvivesRejectedAndCanceledDials(t *testing.T) {
	manager, policy, resolver := recoveryManager(t, nil)
	first := recoveryDial(t, manager, policy)
	defer first.Close()
	firstID := recoveryIdentity(t, first)
	if conn, err := manager.DialContext(t.Context(), policy, "tcp", "203.0.113.10:9"); err == nil {
		conn.Close()
		t.Fatal("synthetic rejected target unexpectedly connected")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if conn, err := manager.DialContext(canceled, policy, "tcp", "203.0.113.10:443"); err == nil {
		conn.Close()
		t.Fatal("canceled dial unexpectedly connected")
	}
	held, cancelHeld := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancelHeld()
	if conn, err := manager.DialContext(held, policy, "tcp", "203.0.113.10:444"); err == nil {
		conn.Close()
		t.Fatal("held negotiation ignored caller cancellation")
	}
	second := recoveryDial(t, manager, policy)
	defer second.Close()
	if nextID := recoveryIdentity(t, second); nextID != firstID || resolver.reads.Load() != 1 {
		t.Fatalf("request failure replaced a live core: before=%+v after=%+v", firstID, nextID)
	}
	if _, err := io.WriteString(first, "echo:still-open\n"); err != nil {
		t.Fatal(err)
	}
	if got, err := bufio.NewReader(first).ReadString('\n'); err != nil || got != "echo:still-open\n" {
		t.Fatalf("independent failed dial interrupted a live stream: %q, %v", got, err)
	}
}

func TestCanceledCoreStartupDoesNotPoisonLaterDial(t *testing.T) {
	store := &recoveryCancelStore{tlsStore: &tlsStore{items: map[string][]byte{}}, entered: make(chan struct{})}
	manager, policy, _ := recoveryManager(t, store)
	caller, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		conn, err := manager.DialContext(caller, policy, "tcp", "203.0.113.10:443")
		if conn != nil {
			conn.Close()
		}
		result <- err
	}()
	awaitLifecycle(t, store.entered, "cancelable first secret read")
	cancel()
	if err := awaitLifecycle(t, result, "canceled first startup"); err == nil {
		t.Fatal("canceled startup returned a connection")
	}
	later := recoveryDial(t, manager, policy)
	defer later.Close()
	if id := recoveryIdentity(t, later); id.pid <= 0 {
		t.Fatal("later caller did not start a usable core")
	}
}

type recoveryPeer struct {
	pid    int
	server string
}

func recoveryIdentity(t *testing.T, conn net.Conn) recoveryPeer {
	t.Helper()
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, "identity\n"); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	var peer recoveryPeer
	if _, err := fmt.Sscan(line, &peer.pid, &peer.server); err != nil {
		t.Fatalf("invalid synthetic identity: %v", err)
	}
	return peer
}

func recoveryDial(t *testing.T, manager *Manager, policy egressnetwork.ProxyPolicy) net.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	conn, err := manager.DialContext(ctx, policy, "tcp", "203.0.113.10:443")
	if err != nil {
		t.Fatalf("synthetic managed dial: %v", err)
	}
	return conn
}

func recoveryManager(t *testing.T, store secretstore.Store) (*Manager, egressnetwork.ProxyPolicy, *recoveryResolver) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("isolated core executable wrapper requires a POSIX shell")
	}
	if store == nil {
		store = &tlsStore{items: map[string][]byte{}}
	}
	profiles, err := egressprofile.NewManager(&tlsRepository{revisions: map[egressprofile.Revision]egressprofile.ProfileRevision{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "synthetic-core")
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	script := "#!/bin/sh\nexec " + quote(self) + " -test.run='^TestManagedCoreRecoveryProcess$' -- \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	resolver := &recoveryResolver{}
	resolver.address.Store("192.0.2.10")
	manager, err := New(Options{Store: store, Profiles: profiles, Owner: t.Context(), Executable: wrapper, SystemResolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	profile, err := manager.Import(t.Context(), tlsCommand())
	if err != nil {
		t.Fatal(err)
	}
	return manager, profile.Policy.Proxy, resolver
}

type recoveryResolver struct {
	address atomic.Value
	reads   atomic.Int32
}

func (r *recoveryResolver) LookupNetIP(_ context.Context, network, host string) ([]netip.Addr, error) {
	if network != "ip" || host != "node.fixture" {
		return nil, errors.New("unexpected synthetic bootstrap")
	}
	r.reads.Add(1)
	return []netip.Addr{netip.MustParseAddr(r.address.Load().(string))}, nil
}

type recoveryCancelStore struct {
	*tlsStore
	entered chan struct{}
	first   atomic.Bool
}

func (s *recoveryCancelStore) ReadAtRevision(ctx context.Context, ref secretstore.Reference, revision secretstore.Revision) (*secretstore.Value, error) {
	if s.first.CompareAndSwap(false, true) {
		close(s.entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return s.tlsStore.ReadAtRevision(ctx, ref, revision)
}

// TestManagedCoreRecoveryProcess implements only the private child-process
// boundary. It never contacts a VLESS server or any external destination.
func TestManagedCoreRecoveryProcess(t *testing.T) {
	marker := -1
	for i, value := range os.Args {
		if value == "--" {
			marker = i
			break
		}
	}
	if marker < 0 {
		return
	}
	args := os.Args[marker+1:]
	if len(args) != 3 || args[0] != "run" || args[1] != "-c" {
		os.Exit(2)
	}
	data, err := os.ReadFile(args[2])
	if err != nil {
		os.Exit(2)
	}
	var config struct {
		Inbounds []struct {
			Protocol string
			Listen   string
			Port     int
			Settings struct{ Accounts []struct{ User, Pass string } }
		}
		Outbounds []struct {
			Settings struct{ Vnext []struct{ Address string } }
		}
	}
	if json.Unmarshal(data, &config) != nil || len(config.Inbounds) == 0 || len(config.Inbounds[0].Settings.Accounts) != 1 || len(config.Outbounds) != 1 || len(config.Outbounds[0].Settings.Vnext) != 1 {
		os.Exit(2)
	}
	inbound := config.Inbounds[0]
	if inbound.Protocol != "socks" || inbound.Listen != "127.0.0.1" {
		os.Exit(2)
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort(inbound.Listen, strconv.Itoa(inbound.Port)))
	if err != nil {
		os.Exit(2)
	}
	defer listener.Close()
	account := inbound.Settings.Accounts[0]
	server := config.Outbounds[0].Settings.Vnext[0].Address
	var clients sync.WaitGroup
	defer clients.Wait()
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		clients.Add(1)
		go func() {
			defer clients.Done()
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(8 * time.Second))
			reader := bufio.NewReader(conn)
			header := make([]byte, 3)
			if _, err := io.ReadFull(reader, header); err != nil || string(header) != string([]byte{5, 1, 2}) {
				return
			}
			conn.Write([]byte{5, 2})
			if _, err := io.ReadFull(reader, header[:2]); err != nil || header[0] != 1 {
				return
			}
			username := make([]byte, int(header[1]))
			if _, err := io.ReadFull(reader, username); err != nil {
				return
			}
			size, err := reader.ReadByte()
			if err != nil {
				return
			}
			password := make([]byte, int(size))
			if _, err := io.ReadFull(reader, password); err != nil || string(username) != account.User || string(password) != account.Pass {
				return
			}
			conn.Write([]byte{1, 0})
			request := make([]byte, 10)
			if _, err := io.ReadFull(reader, request); err != nil || request[0] != 5 || request[1] != 1 || request[3] != 1 {
				return
			}
			if request[8] == 0 && request[9] == 9 {
				conn.Write([]byte{5, 5, 0})
				return
			}
			if request[8] == 1 && request[9] == 188 { // 444: wait for cancellation.
				io.Copy(io.Discard, reader)
				return
			}
			conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				switch line {
				case "identity\n":
					fmt.Fprintf(conn, "%d %s\n", os.Getpid(), server)
				case "exit\n":
					os.Exit(0)
				default:
					io.WriteString(conn, line)
				}
			}
		}()
	}
}
