package serverhost

import (
	"errors"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/ipallowlist"
	"github.com/vibe-agi/vibermate/internal/servercontrol"
)

// ipAllowlistReloadInterval bounds how long a list cleared on the Server
// machine takes to reach a running Server.
const ipAllowlistReloadInterval = 2 * time.Second

// ipAllowlist saves the Owner's list and puts it in effect as one step, so
// concurrent saves and file reloads can never leave the gate on an older
// list than the one stored.
type ipAllowlist struct {
	mu    sync.Mutex
	store *ipallowlist.Store
	gate  *connectionGate
}

func (allowlist *ipAllowlist) Current() ipallowlist.Snapshot {
	return allowlist.store.Current()
}

func (allowlist *ipAllowlist) Replace(
	expected int64,
	list ipallowlist.List,
) (ipallowlist.Snapshot, error) {
	allowlist.mu.Lock()
	defer allowlist.mu.Unlock()
	saved, err := allowlist.store.Replace(expected, list)
	switch {
	case err == nil:
		allowlist.gate.update(saved.List)
	case errors.Is(err, ipallowlist.ErrConflict):
		// Replace re-read the file; follow whatever is stored now.
		allowlist.gate.update(allowlist.store.Current().List)
	}
	return saved, err
}

func (allowlist *ipAllowlist) Refusals() servercontrol.IPRefusals {
	refusals := allowlist.gate.refusals()
	return servercontrol.IPRefusals{
		Count: refusals.count, LastAddress: refusals.lastAddress, LastAt: refusals.lastAt,
		ProxyHeaderProblems: refusals.proxyHeaderProblems,
	}
}

func (allowlist *ipAllowlist) TrustedProxies() ipallowlist.List {
	return allowlist.gate.trusted
}

// reload applies a list changed on disk, such as one cleared with
// `vibermated server ip-allowlist clear`. A broken file keeps the list in
// effect rather than opening the Server to every address.
func (allowlist *ipAllowlist) reload() {
	allowlist.mu.Lock()
	defer allowlist.mu.Unlock()
	snapshot, changed, err := allowlist.store.Reload()
	if err == nil && changed {
		allowlist.gate.update(snapshot.List)
	}
}

func (allowlist *ipAllowlist) watch(stop <-chan struct{}, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			allowlist.reload()
		}
	}
}
