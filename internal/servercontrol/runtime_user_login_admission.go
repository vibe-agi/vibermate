package servercontrol

import (
	"net"
	"strconv"
	"sync"
	"time"
)

const (
	maximumConcurrentPasswordChecks = 4
	maximumLoginsPerPeerPerMinute   = 10
	maximumLoginsGloballyPerMinute  = 120
	maximumTrackedLoginPeers        = 4096
)

type runtimeUserLoginWindow struct {
	started time.Time
	count   int
}

// runtimeUserLoginAdmission bounds both the instantaneous Argon2 working set
// and the amount of password work an unauthenticated peer can cause over time.
// It deliberately keys only from the TCP peer and never trusts forwarding
// headers supplied by the caller.
type runtimeUserLoginAdmission struct {
	mu      sync.Mutex
	global  runtimeUserLoginWindow
	peers   map[string]runtimeUserLoginWindow
	working chan struct{}
	now     func() time.Time
}

func newRuntimeUserLoginAdmission() *runtimeUserLoginAdmission {
	return &runtimeUserLoginAdmission{
		peers:   make(map[string]runtimeUserLoginWindow),
		working: make(chan struct{}, maximumConcurrentPasswordChecks),
		now:     time.Now,
	}
}

func (admission *runtimeUserLoginAdmission) acquire(
	remoteAddress string,
) (func(), time.Duration, bool) {
	if admission == nil {
		return nil, time.Minute, false
	}
	now := admission.now().UTC()
	peer := runtimeUserLoginPeer(remoteAddress)

	admission.mu.Lock()
	if admission.global.started.IsZero() ||
		now.Sub(admission.global.started) >= time.Minute ||
		now.Before(admission.global.started) {
		admission.global = runtimeUserLoginWindow{started: now}
		clear(admission.peers)
	}
	window, found := admission.peers[peer]
	if !found && len(admission.peers) >= maximumTrackedLoginPeers {
		peer = "overflow"
		window = admission.peers[peer]
	}
	if window.started.IsZero() || now.Sub(window.started) >= time.Minute ||
		now.Before(window.started) {
		window = runtimeUserLoginWindow{started: now}
	}
	if admission.global.count >= maximumLoginsGloballyPerMinute {
		retry := admission.global.started.Add(time.Minute).Sub(now)
		admission.mu.Unlock()
		return nil, positiveRetryAfter(retry), false
	}
	if window.count >= maximumLoginsPerPeerPerMinute {
		retry := window.started.Add(time.Minute).Sub(now)
		admission.mu.Unlock()
		return nil, positiveRetryAfter(retry), false
	}
	admission.global.count++
	window.count++
	admission.peers[peer] = window
	admission.mu.Unlock()

	select {
	case admission.working <- struct{}{}:
		var once sync.Once
		return func() {
			once.Do(func() { <-admission.working })
		}, 0, true
	default:
		return nil, time.Second, false
	}
}

func runtimeUserLoginPeer(remoteAddress string) string {
	host, _, err := net.SplitHostPort(remoteAddress)
	if err != nil {
		return "unknown"
	}
	address := net.ParseIP(host)
	if address == nil {
		return "unknown"
	}
	return address.String()
}

func positiveRetryAfter(duration time.Duration) time.Duration {
	if duration <= 0 {
		return time.Second
	}
	return duration
}

func retryAfterHeader(duration time.Duration) string {
	seconds := int64((positiveRetryAfter(duration) + time.Second - 1) / time.Second)
	return strconv.FormatInt(seconds, 10)
}
