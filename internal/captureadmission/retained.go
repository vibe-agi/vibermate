package captureadmission

import (
	"context"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureauthority"
)

type CurrentAuthority interface {
	Authorizer
	Revalidate(context.Context, Admission) (captureauthority.Validity, error)
}

type retainedKey struct {
	reference string
	revision  uint64
}
type subscription struct {
	receipt   *retainedReceipt
	cancel    func()
	done      chan struct{}
	withdrawn bool
}
type retainedEntry struct {
	admission        Admission
	validity         captureauthority.Validity
	version, checked uint64
	dirty            bool
	reading          context.CancelFunc
	subscribers      map[*subscription]struct{}
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
func (r *Retained) Retain(ctx context.Context, admission Admission, cancel func()) (func(), error) {
	// Attribution constructors are public; only authenticated receipts may join
	// an existing generation. Never borrow the first subscriber's permission.
	if admission.receipt == nil || admission.receipt.issuer == nil || admission.receipt.withdrawn.Load() || cancel == nil || ctx == nil {
		return nil, ErrCredentialRejected
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
			release()
			return nil, ErrCredentialRejected
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
			release()
			return nil, ctx.Err()
		}
	}
}
func (r *Retained) markLocked(entry *retainedEntry) {
	entry.version++
	entry.dirty = true
	if entry.reading != nil {
		entry.reading()
	}
	r.signalLocked()
}
func matches(change captureauthority.Change, entry *retainedEntry) bool {
	if entry.checked == 0 {
		return true
	}
	return change.Capture.ID != "" && entry.validity.Capture == change.Capture || change.UserID != "" && entry.validity.UserID == change.UserID || change.SessionDigest != ([32]byte{}) && entry.validity.SessionDigest == change.SessionDigest
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
		if matches(change, entry) {
			r.markLocked(entry)
			t := target{entry: entry, version: entry.version}
			for sub := range entry.subscribers {
				t.subs = append(t.subs, sub)
			}
			targets = append(targets, t)
		}
	}
	r.mu.Unlock()
	return func(ctx context.Context) error {
		for _, target := range targets {
			for {
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
			if selected == nil && entry.dirty {
				key, selected = k, entry
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
			selected.checked = version
			selected.dirty = false
			if err != nil {
				delete(r.entries, key)
				for sub := range selected.subscribers {
					sub.withdrawn = true
					sub.receipt.withdrawn.Store(true)
					cancellations = append(cancellations, sub.cancel)
				}
			} else {
				selected.validity = validity
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
