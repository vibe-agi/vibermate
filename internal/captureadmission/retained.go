package captureadmission

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureauthority"
)

type CurrentAuthority interface {
	Authorizer
	Revalidate(context.Context, Admission) (captureauthority.Validity, error)
}

// A validity read that only timed out says the control store is slow, not
// that the Capture lost its authority. Work already running keeps its last
// proven validity for this long while the read is retried. A read that fails
// for any other reason, a first read, and a read prompted by a Change stay
// fail-closed.
const (
	revalidationStallGrace = 30 * time.Second
	revalidationStallRetry = time.Second
)

type retainedKey struct {
	reference string
	revision  uint64
}
type subscription struct {
	entry     *retainedEntry
	receipt   *retainedReceipt
	cancel    func()
	done      chan struct{}
	withdrawn bool
}
type retainedEntry struct {
	admission        Admission
	validity         captureauthority.Validity
	validityKnown    bool
	version, checked uint64
	dirty            bool
	// hinted is set by a Change and cleared by the next successful read: an
	// explicit revocation hint is never waited out.
	hinted       bool
	stalledSince time.Time
	retryAt      time.Time
	reading      context.CancelFunc
	subscribers  map[*subscription]struct{}
}

// toleratesStall reports whether a failed re-read may be retried without
// withdrawing the work that the last successful read admitted.
func (entry *retainedEntry) toleratesStall(err error, now time.Time) bool {
	// A read that timed out or could not reach storage learned nothing about
	// the credential. A rejection, or a read after an explicit change hint, is
	// never waited out.
	unanswered := errors.Is(err, context.DeadlineExceeded) || errors.Is(err, captureauthority.ErrUnavailable)
	return unanswered &&
		entry.validityKnown && !entry.hinted &&
		(entry.stalledSince.IsZero() || now.Sub(entry.stalledSince) < revalidationStallGrace)
}

// Retained owns connection cancellation and one deadline worker, not permission.
// Every new operation still reads CurrentAuthority. Changes are only hints to
// re-read the exact authenticated receipt; delayed hints cannot kill new grants.
type Retained struct {
	authority     CurrentAuthority
	now           func() time.Time
	ctx           context.Context
	cancel        context.CancelFunc
	mu            sync.Mutex
	entries       map[retainedKey]*retainedEntry
	subscriptions map[*subscription]struct{}
	accepting     bool
	changed       chan struct{}
	wake          chan struct{}
	done          chan struct{}
}

func NewRetained(owner context.Context, authority CurrentAuthority, now func() time.Time) *Retained {
	ctx, cancel := context.WithCancel(owner)
	r := &Retained{authority: authority, now: now, ctx: ctx, cancel: cancel, entries: make(map[retainedKey]*retainedEntry), subscriptions: make(map[*subscription]struct{}), accepting: true, changed: make(chan struct{}), wake: make(chan struct{}, 1), done: make(chan struct{})}
	go r.run()
	return r
}
func (r *Retained) Authorize(ctx context.Context, credential ProxyCredential) (Admission, error) {
	return r.authority.Authorize(ctx, credential)
}
func (r *Retained) Revalidate(ctx context.Context, admission Admission) (captureauthority.Validity, error) {
	r.mu.Lock()
	accepting := r.accepting
	r.mu.Unlock()
	if !accepting || admission.receipt == nil || admission.receipt.withdrawn.Load() {
		return captureauthority.Validity{}, ErrCredentialRejected
	}
	return r.authority.Revalidate(ctx, admission)
}
func (r *Retained) signalLocked() {
	close(r.changed)
	r.changed = make(chan struct{})
	select {
	case r.wake <- struct{}{}:
	default:
	}
}
func keyOf(a Admission) retainedKey { return retainedKey{a.AdmissionRef(), a.CredentialRevision()} }

// Retain registers BEFORE the final source read. Changes fence every running
// read and also match pending entries whose identity indexes are not known yet.
// Once inserted, the returned cleanup handle is non-nil even on admission error.
// The caller must release it only after its outer operation cleanup has finished.
func (r *Retained) Retain(ctx context.Context, admission Admission, cancel func()) (func(), error) {
	// Attribution constructors are public; only authenticated receipts may join
	// an existing generation. Never borrow the first subscriber's permission.
	if admission.receipt == nil || admission.receipt.issuer == nil || admission.receipt.withdrawn.Load() || cancel == nil || ctx == nil {
		return nil, ErrCredentialRejected
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := keyOf(admission)
	sub := &subscription{receipt: admission.receipt, cancel: cancel, done: make(chan struct{})}
	r.mu.Lock()
	if !r.accepting {
		r.mu.Unlock()
		return nil, ErrCredentialRejected
	}
	entry := r.entries[key]
	if entry != nil && entry.admission.receipt.issuer != admission.receipt.issuer {
		r.mu.Unlock()
		return nil, ErrCredentialRejected
	}
	if entry == nil {
		entry = &retainedEntry{admission: admission, subscribers: make(map[*subscription]struct{})}
		r.entries[key] = entry
	}
	sub.entry = entry
	entry.subscribers[sub] = struct{}{}
	r.subscriptions[sub] = struct{}{}
	r.markLocked(entry)
	version := entry.version
	r.mu.Unlock()
	var once sync.Once
	release := func() {
		once.Do(func() {
			r.mu.Lock()
			delete(entry.subscribers, sub)
			delete(r.subscriptions, sub)
			close(sub.done)
			if r.entries[key] == entry && len(entry.subscribers) == 0 {
				delete(r.entries, key)
				if entry.reading != nil {
					entry.reading()
				}
			}
			r.signalLocked()
			r.mu.Unlock()
		})
	}
	for {
		r.mu.Lock()
		if sub.withdrawn || !r.accepting {
			r.mu.Unlock()
			return release, ErrCredentialRejected
		}
		if entry.checked >= version && !entry.dirty {
			r.mu.Unlock()
			return release, nil
		}
		changed := r.changed
		r.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			r.mu.Lock()
			// This request can no longer become active, but its outer cleanup is still
			// owned. Remove it from validity scheduling, not from subscriptions.
			sub.withdrawn = true
			delete(entry.subscribers, sub)
			if r.entries[key] == entry && len(entry.subscribers) == 0 {
				delete(r.entries, key)
				if entry.reading != nil {
					entry.reading()
				}
			}
			r.signalLocked()
			r.mu.Unlock()
			return release, ctx.Err()
		}
	}
}
func (r *Retained) markLocked(entry *retainedEntry) {
	entry.version++
	entry.dirty = true
	entry.retryAt = time.Time{}
	if entry.reading != nil {
		entry.reading()
	}
	r.signalLocked()
}
func matches(change captureauthority.Change, entry *retainedEntry) bool {
	// The sealed admission always identifies its Capture/generation, including
	// when the first validity read failed. User/Session indexes require a
	// successful source read; checked also counts failures and is not that proof.
	capture, _ := entry.admission.CaptureReference()
	if change.Capture.ID != "" && capture == change.Capture {
		return true
	}
	if change.UserID == "" && change.SessionDigest == ([32]byte{}) {
		return false
	}
	if !entry.validityKnown {
		return true
	}
	return change.UserID != "" && entry.validity.UserID == change.UserID || change.SessionDigest != ([32]byte{}) && entry.validity.SessionDigest == change.SessionDigest
}

// BeginChange fences work immediately. Its wait joins withdrawn subscriptions
// only after all cancellation owners (including native MCP) have been notified.
func (r *Retained) BeginChange(change captureauthority.Change) func(context.Context) error {
	type target struct {
		entry   *retainedEntry
		version uint64
		subs    []*subscription
	}
	var targets []target
	r.mu.Lock()
	for _, entry := range r.entries {
		if r.accepting && matches(change, entry) {
			r.markLocked(entry)
			entry.hinted = true
			t := target{entry: entry, version: entry.version}
			for sub := range entry.subscribers {
				t.subs = append(t.subs, sub)
			}
			targets = append(targets, t)
		}
	}
	// Retired entries no longer participate in validity scheduling, but their
	// subscriptions still own cleanup. Join those exact owners without reading
	// again, reviving them, or borrowing a replacement generation's facts.
	var retired []*subscription
	for sub := range r.subscriptions {
		if sub.withdrawn && matches(change, sub.entry) {
			retired = append(retired, sub)
		}
	}
	if len(retired) != 0 {
		targets = append(targets, target{subs: retired})
	}
	r.mu.Unlock()
	return func(ctx context.Context) error {
		for _, target := range targets {
			for target.entry != nil {
				r.mu.Lock()
				complete := target.entry.checked >= target.version || len(target.entry.subscribers) == 0 || !r.accepting
				changed := r.changed
				r.mu.Unlock()
				if complete {
					break
				}
				select {
				case <-changed:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			for _, sub := range target.subs {
				r.mu.Lock()
				withdrawn := sub.withdrawn
				r.mu.Unlock()
				if withdrawn {
					select {
					case <-sub.done:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
			}
		}
		return nil
	}
}
func (r *Retained) Changed(ctx context.Context, change captureauthority.Change) error {
	return r.BeginChange(change)(ctx)
}

func (r *Retained) run() {
	defer close(r.done)
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		r.mu.Lock()
		if !r.accepting {
			r.mu.Unlock()
			return
		}
		var selected *retainedEntry
		var key retainedKey
		delay := time.Hour
		now := r.now()
		for k, entry := range r.entries {
			if !entry.dirty && !entry.validity.ExpiresAt.IsZero() {
				remaining := entry.validity.ExpiresAt.Sub(now)
				if remaining <= 0 {
					r.markLocked(entry)
				} else if remaining < delay {
					delay = remaining
				}
			}
			if entry.dirty {
				if wait := entry.retryAt.Sub(now); wait > 0 {
					delay = min(delay, wait)
				} else if selected == nil {
					key, selected = k, entry
				}
			}
		}
		if selected == nil {
			r.mu.Unlock()
			timer.Reset(delay)
			select {
			case <-r.wake:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
			case <-timer.C:
			case <-r.ctx.Done():
				r.BeginShutdown()
			}
			continue
		}
		version := selected.version
		ctx, cancel := context.WithTimeout(r.ctx, 2*time.Second)
		selected.reading = cancel
		r.mu.Unlock()
		validity, err := r.authority.Revalidate(ctx, selected.admission)
		cancel()
		r.mu.Lock()
		var cancellations []func()
		if r.entries[key] == selected && selected.version == version && r.accepting {
			selected.reading = nil
			if now := r.now(); err != nil && selected.toleratesStall(err, now) {
				// Still dirty and unchecked: nothing new is admitted on this
				// entry until a read succeeds.
				if selected.stalledSince.IsZero() {
					selected.stalledSince = now
				}
				selected.retryAt = now.Add(revalidationStallRetry)
			} else {
				selected.checked = version
				selected.dirty = false
				selected.stalledSince, selected.retryAt = time.Time{}, time.Time{}
				if err != nil {
					delete(r.entries, key)
					for sub := range selected.subscribers {
						sub.withdrawn = true
						sub.receipt.withdrawn.Store(true)
						cancellations = append(cancellations, sub.cancel)
					}
				} else {
					selected.validity = validity
					selected.validityKnown = true
					selected.hinted = false
				}
			}
			r.signalLocked()
		}
		r.mu.Unlock()
		for _, cancel := range cancellations {
			cancel()
		}
	}
}
func (r *Retained) BeginShutdown() {
	r.mu.Lock()
	if !r.accepting {
		r.mu.Unlock()
		return
	}
	r.accepting = false
	r.cancel()
	var cancellations []func()
	for _, entry := range r.entries {
		for sub := range entry.subscribers {
			sub.withdrawn = true
			sub.receipt.withdrawn.Store(true)
			cancellations = append(cancellations, sub.cancel)
		}
	}
	r.signalLocked()
	r.mu.Unlock()
	for _, cancel := range cancellations {
		cancel()
	}
}
func (r *Retained) Shutdown(ctx context.Context) error {
	r.BeginShutdown()
	select {
	case <-r.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	r.mu.Lock()
	var pending []<-chan struct{}
	for sub := range r.subscriptions {
		pending = append(pending, sub.done)
	}
	r.mu.Unlock()
	for _, done := range pending {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

var _ CurrentAuthority = (*Retained)(nil)
