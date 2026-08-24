package serverdaemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/hostsecret"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/serverhost"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

type synchronizedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (buffer *synchronizedBuffer) Write(value []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.Write(value)
}

func (buffer *synchronizedBuffer) snapshot() []byte {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return append([]byte(nil), buffer.buffer.Bytes()...)
}

func TestRunPublishesCapabilityFreeReadyStatus(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	paths, _ := productruntime.NewRuntimePaths(filepath.Join(root, "data"))
	secretsFactory, _ := hostsecret.NewDevelopmentFileFactory(filepath.Join(root, "secrets", "store.json"))
	secrets, _ := secretsFactory.Open(context.Background())
	coordinator, _ := offlinehold.New(offlinehold.DefaultConfig())
	hostOptions := serverhost.DefaultOptions(productruntime.Options{
		Paths: paths, Host: hostcontract.Server(), OfflineHold: coordinator, Secrets: secrets,
		Approvals: toolapproval.DefaultConfig(), ExchangeHold: exchange.DefaultHoldPolicy(),
		Clock: productruntime.SystemClock{}, InstanceIDs: productruntime.NewCryptographicInstanceIDSource(),
		SecurityRandom: rand.Reader, Lifecycle: productruntime.DefaultLifecycleOptions(),
	})
	hostOptions.ListenAddress = "127.0.0.1:0"
	var ready synchronizedBuffer
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Host: hostOptions, ReadyWriter: &ready, ShutdownTimeout: 10 * time.Second})
	}()
	deadline := time.Now().Add(10 * time.Second)
	var published []byte
	for len(published) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		published = ready.snapshot()
	}
	if len(published) == 0 {
		t.Fatal("Server daemon did not publish readiness")
	}
	if bytes.Contains(published, []byte("controlToken")) ||
		bytes.Contains(published, []byte("proxyToken")) {
		t.Fatalf("readiness leaked capability: %s", published)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
