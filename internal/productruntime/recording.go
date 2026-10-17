package productruntime

import (
	"context"
	"errors"
	"github.com/vibe-agi/vibermate/internal/evidencearchive"
	"github.com/vibe-agi/vibermate/internal/nativemcp"
	"github.com/vibe-agi/vibermate/internal/resourcedeletion"
	"math"
	"reflect"
	"strings"
	"sync"
	"unsafe"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/recording"
)

// One finite queue owns only sanitized value snapshots. It shares Raw's budget.
// Each sequential store call is a batch of one (below the 64 item/4 MiB target).
type recordingItem struct {
	kind        recording.Kind
	stamp       recording.Stamp
	connection  connectionevent.Event
	egress      egressaudit.Attempt
	reservation *recording.Reservation
	bytes       int64
}
type observationRecorder struct {
	budget           *recording.Budget
	epoch            *recording.Epoch
	connections      connectionevent.ScopedStore
	egress           egressaudit.ScopedAttemptStore
	queue            chan *recordingItem
	ctx              context.Context
	cancel           context.CancelFunc
	done             chan struct{}
	mu               sync.Mutex
	closed, sealed   bool
	offered, settled uint64
	changed          chan struct{}
}

func newObservationRecorder(owner context.Context, b *recording.Budget, c connectionevent.ScopedStore, e egressaudit.ScopedAttemptStore) *observationRecorder {
	epoch, _ := recording.NewEpoch(1)
	ctx, cancel := context.WithCancel(owner)
	r := &observationRecorder{budget: b, epoch: epoch, connections: c, egress: e, queue: make(chan *recordingItem, 2048), ctx: ctx, cancel: cancel, done: make(chan struct{}), changed: make(chan struct{})}
	go r.run()
	return r
}

// ScopeStamp is called only alongside already admitted/frozen work. It confers
// no execution authority and creates no registry. Durable generations follow
// in the storage-role split; the current terminal barrier owns Capture drains.
func (r *observationRecorder) ScopeStamp(c captureidentity.Reference) recording.Stamp {
	generation := uint64(0)
	if c != (captureidentity.Reference{}) {
		generation = 1
	}
	t, err := recording.NewScopeToken(r.epoch, c, recording.Fence{ArchiveEpoch: r.epoch.Value(), CaptureGeneration: generation})
	if err != nil {
		return recording.Stamp{}
	}
	return t.Stamp()
}
func (r *observationRecorder) ObserveConnection(s recording.Stamp, e connectionevent.Event) recording.OfferResult {
	return r.offer(s, recording.Connection, e, egressaudit.Attempt{})
}
func (r *observationRecorder) ObserveEgress(s recording.Stamp, a egressaudit.Attempt) recording.OfferResult {
	return r.offer(s, recording.Egress, connectionevent.Event{}, a)
}
func (r *observationRecorder) offer(s recording.Stamp, k recording.Kind, e connectionevent.Event, a egressaudit.Attempt) (result recording.OfferResult) {
	bytes := int64(0)
	counted := false
	defer func() {
		if !counted {
			r.budget.RecordOffer(k, result, uint64(bytes))
		}
	}()
	if !s.Current() {
		return recording.DroppedStale
	}
	r.mu.Lock()
	closed := r.closed || r.sealed
	r.mu.Unlock()
	if closed {
		return recording.DroppedClosed
	}
	if k == recording.Connection && r.connections == nil || k == recording.Egress && r.egress == nil {
		return recording.DroppedUnavailable
	}
	if k == recording.Connection && e.Validate() != nil || k == recording.Egress && (a.ID() == "" || a.Purpose() == egressaudit.PurposeClientToolService) {
		return recording.DroppedInvalid
	}
	// Descriptor, complete owned strings, scope/token, and conservative SQL/
	// driver argument workspace stay charged through actual I/O completion.
	bytes = int64(unsafe.Sizeof(recordingItem{})) + int64(len(s.Capture().ID)) + 256
	if k == recording.Connection {
		bytes += connectionStringBytes(e)
	} else {
		bytes += attemptStringBytes(a)
	}
	if bytes > 64<<20 {
		return recording.DroppedTooLarge
	}
	reservation, ok := r.budget.TryReserve(k, bytes+8192+2*bytes)
	if !ok {
		return recording.DroppedCapacity
	}
	item := &recordingItem{kind: k, stamp: s, reservation: reservation, bytes: bytes}
	if k == recording.Connection {
		item.connection = cloneConnection(e)
	} else {
		copied, err := cloneAttempt(a)
		if err != nil {
			reservation.Release()
			return recording.DroppedInvalid
		}
		item.egress = copied
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.sealed {
		reservation.Release()
		return recording.DroppedClosed
	}
	if !s.Current() {
		reservation.Release()
		return recording.DroppedStale
	}
	select {
	case r.queue <- item:
		r.budget.RecordOffer(k, recording.Accepted, uint64(bytes))
		counted = true
		r.offered++
		return recording.Accepted
	default:
		reservation.Release()
		return recording.DroppedCapacity
	}
}
func connectionStringBytes(e connectionevent.Event) int64 {
	v := reflect.ValueOf(e)
	var n int64
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).Kind() == reflect.String {
			n += int64(v.Field(i).Len())
		}
	}
	return n
}
func cloneConnection(e connectionevent.Event) connectionevent.Event {
	v := reflect.ValueOf(&e).Elem()
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if f.Kind() == reflect.String {
			f.SetString(strings.Clone(f.String()))
		}
	}
	e.StartedAt = e.StartedAt.UTC()
	e.EndedAt = e.EndedAt.UTC()
	return e
}
func attemptStringBytes(a egressaudit.Attempt) int64 {
	p, d := a.Parent(), a.Decision()
	return int64(len(a.ID()) + len(a.ConnectionID()) + len(a.Purpose()) + len(a.PayloadClass()) + len(p.Kind) + len(p.ID) + len(p.ExchangeID) + len(a.Caller()) + len(a.CallerID()) + len(a.TargetOrigin()) + len(d.SelectionSource) + len(d.PolicyID) + len(d.Authority) + len(d.RuleID) + len(d.ProxyID) + len(d.AccountID) + len(a.Outcome()) + len(a.ErrorClass()))
}
func cloneAttempt(a egressaudit.Attempt) (egressaudit.Attempt, error) {
	p, d := a.Parent(), a.Decision()
	p.Kind = egressaudit.ParentKind(strings.Clone(string(p.Kind)))
	p.ID = strings.Clone(p.ID)
	p.ExchangeID = strings.Clone(p.ExchangeID)
	// Clone all string-backed fields including named closed values. New/Finish
	// revalidate the complete snapshot without retaining any transport object.
	dv := reflect.ValueOf(&d).Elem()
	for i := 0; i < dv.NumField(); i++ {
		f := dv.Field(i)
		if f.Kind() == reflect.String {
			f.SetString(strings.Clone(f.String()))
		}
	}
	c, err := egressaudit.New(egressaudit.NewInput{ID: strings.Clone(a.ID()), ConnectionID: strings.Clone(a.ConnectionID()), Purpose: egressaudit.EgressPurpose(strings.Clone(string(a.Purpose()))), PayloadClass: egressaudit.PayloadClass(strings.Clone(string(a.PayloadClass()))), Parent: p, Caller: egressaudit.CallerKind(strings.Clone(string(a.Caller()))), CallerID: strings.Clone(a.CallerID()), TargetOrigin: strings.Clone(a.TargetOrigin()), Decision: d, ReusedTransport: a.ReusedTransport(), StartedAt: a.StartedAt().UTC()})
	if err != nil {
		return c, err
	}
	if a.Terminal() {
		return c.Finish(egressaudit.TerminalInput{Outcome: egressaudit.Outcome(strings.Clone(string(a.Outcome()))), ErrorClass: strings.Clone(a.ErrorClass()), BytesOut: a.BytesOut(), BytesIn: a.BytesIn(), CompletedAt: a.CompletedAt().UTC(), UnobservedByteCounts: !a.ByteCountsKnown()})
	}
	return c, nil
}
func (r *observationRecorder) run() {
	defer close(r.done)
	for {
		select {
		case <-r.ctx.Done():
			r.mu.Lock()
			r.closed = true
			r.mu.Unlock()
			for {
				select {
				case item := <-r.queue:
					r.settle(item, recording.Discarded)
				default:
					return
				}
			}
		case item := <-r.queue:
			outcome := recording.Discarded
			if item.stamp.Current() && r.ctx.Err() == nil {
				var err error
				if item.kind == recording.Connection {
					err = r.connections.PutConnection(r.ctx, item.stamp.Scope(), item.connection)
				} else {
					err = r.egress.PutScopedAttempt(r.ctx, item.stamp.Scope(), item.egress)
				}
				outcome = recording.Persisted
				if err != nil {
					outcome = recording.Unconfirmed
				}
			}
			r.settle(item, outcome)
		}
	}
}
func (r *observationRecorder) settle(item *recordingItem, outcome recording.Settlement) {
	r.budget.RecordSettlement(item.kind, outcome, uint64(item.bytes))
	item.reservation.Release()
	r.mu.Lock()
	r.settled++
	close(r.changed)
	r.changed = make(chan struct{})
	r.mu.Unlock()
}
func (r *observationRecorder) Flush(ctx context.Context) error {
	r.mu.Lock()
	target := r.offered
	r.mu.Unlock()
	return r.wait(ctx, target)
}
func (r *observationRecorder) wait(ctx context.Context, target uint64) error {
	for {
		r.mu.Lock()
		done := r.settled >= target
		changed := r.changed
		r.mu.Unlock()
		if done {
			return nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
func (r *observationRecorder) beginClear(ctx context.Context) (func(), error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, errors.New("recorder closed")
	}
	r.sealed = true
	target := r.offered
	r.mu.Unlock()
	if err := r.wait(ctx, target); err != nil {
		return nil, err
	} // leave the recording seal held until a retry actually joins
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			value := r.epoch.Value()
			if value == math.MaxUint64 {
				r.closed = true
				return
			}
			r.epoch.Advance(value + 1)
			r.sealed = false
		})
	}, nil
}
func (r *observationRecorder) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	r.closed = true
	r.cancel()
	r.mu.Unlock()
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type recordingArchiveBarrier struct {
	capture interface {
		BeginClear(context.Context) (evidencearchive.Release, error)
	}
	recorder *observationRecorder
}

func (b recordingArchiveBarrier) BeginClear(ctx context.Context) (evidencearchive.Release, error) {
	release, err := b.capture.BeginClear(ctx)
	if err != nil {
		return nil, err
	}
	if b.recorder == nil {
		return release, nil
	}
	reopen, err := b.recorder.beginClear(ctx)
	if err != nil {
		release()
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { reopen(); release() }) }, nil
}

type observationRecorderBuilder func(context.Context, *recording.Budget, connectionevent.ScopedStore, egressaudit.ScopedAttemptStore) *observationRecorder

// Administrative deletion follows revocation/holder checks. Rejoining the
// terminal producers here covers callbacks that run after Capture Finish's
// earlier Raw barrier. No live forwarding path calls this method.
type recordingArchive struct {
	raw      rawEvidenceRuntime
	delegate resourcedeletion.Archive
	recorder *observationRecorder
	revoker  *nativeRevocation
}

func (a recordingArchive) DeleteCapture(ctx context.Context, kind, id string) (resourcedeletion.Released, error) {
	capture, err := captureidentity.New(captureidentity.Kind(kind), id)
	if err != nil {
		return resourcedeletion.Released{}, err
	}
	if a.revoker != nil {
		if err := a.revoker.Revoke(ctx, capture, string(nativemcp.RevocationCaptureStopped)); err != nil {
			return resourcedeletion.Released{}, err
		}
	}
	var terminal rawevidence.TerminalScope
	if a.raw != nil {
		terminal, err = a.raw.PrepareTerminalScope(ctx, rawevidence.ScopeKind(kind), id)
		if err != nil {
			return resourcedeletion.Released{}, err
		}
		defer terminal.Abort()
	}
	if err := a.recorder.Flush(ctx); err != nil {
		return resourcedeletion.Released{}, err
	}
	released, err := a.delegate.DeleteCapture(ctx, kind, id)
	if err == nil && terminal != nil {
		terminal.Commit()
	}
	return released, err
}
func (a recordingArchive) ClearEvidence(ctx context.Context) (resourcedeletion.Released, error) {
	return a.delegate.ClearEvidence(ctx)
}
