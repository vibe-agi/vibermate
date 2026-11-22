package desktophost

import (
	"context"
	"crypto/rand"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/hostsecret"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/serverhost"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

// The Runtime's index and the attached Server's companion check read the
// Desktop's own catalog, so a companion claim names the client the index
// knows. DefaultAttachOptions alone would give the Server BuiltInCatalog.
func TestAttachedServerValidatesCompanionsAgainstTheDesktopCatalog(t *testing.T) {
	t.Parallel()

	custom, err := clientadapter.NewCatalogWithSigners(
		clientadapter.BuiltInCatalog().Revision()-1,
		[]clientadapter.Release{clientadapter.ClaudeCode221220DarwinARM64()},
		[]clientadapter.Signer{clientadapter.ClaudeCodeSignerDarwin()},
	)
	if err != nil {
		t.Fatal(err)
	}
	want, err := custom.Digest()
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	paths, err := NewPaths(filepath.Join(root, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	runtimePaths, err := productruntime.NewRuntimePaths(filepath.Join(root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	factory, err := hostsecret.NewServerFileFactory(runtimePaths.DataDirectory())
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := factory.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	gate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	options := DefaultOptions(paths, productruntime.Options{
		Paths: runtimePaths, Host: hostcontract.Desktop(), SecurityRandom: rand.Reader,
		Secrets: secrets, OfflineHold: gate, Approvals: toolapproval.DefaultConfig(),
		ExchangeHold: exchange.DefaultHoldPolicy(), Clock: productruntime.SystemClock{},
		InstanceIDs: productruntime.NewCryptographicInstanceIDSource(),
		Lifecycle:   productruntime.DefaultLifecycleOptions(),
	})
	options.ClientCatalog = custom
	options.RemoteServerEnabled = true
	options.RemoteServerListenAddress = "127.0.0.1:0"
	options.ShutdownTimeout = 15 * time.Second

	var runtimeCatalog, serverCatalog clientadapter.Catalog
	stop := errors.New("captured the attach options")
	startRuntime := func(ctx context.Context, runtimeOptions productruntime.Options) (*productruntime.Runtime, error) {
		runtimeCatalog = runtimeOptions.ClientCatalog
		return productruntime.Start(ctx, runtimeOptions)
	}
	attachServer := func(_ context.Context, attachOptions serverhost.AttachOptions) (*serverhost.Host, error) {
		serverCatalog = attachOptions.ClientCatalog
		return nil, stop
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	host, err := startWithConstructors(ctx, options, startRuntime, attachServer)
	if host != nil || !errors.Is(err, stop) {
		t.Fatalf("startWithConstructors() = %v, %v", host, err)
	}
	for name, catalog := range map[string]clientadapter.Catalog{
		"Runtime":         runtimeCatalog,
		"attached Server": serverCatalog,
	} {
		got, err := catalog.Digest()
		if err != nil || got != want || catalog.Revision() != custom.Revision() {
			t.Fatalf("the %s catalog is revision %d digest %q (%v), want the Desktop's revision %d",
				name, catalog.Revision(), got, err, custom.Revision())
		}
	}
}
