package productruntime

import (
	"context"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/recording"
	"sync"
	"testing"
)

// EgressOfferProbe records actual producer calls and delegates without waiting.
// These values are send-entry evidence, never persisted inspection results.
type EgressOfferProbe struct {
	mu       sync.Mutex
	delegate *runtimeEgressRepository
	attempts map[string]egressaudit.Attempt
}

func (p *EgressOfferProbe) wrap(delegate *runtimeEgressRepository) egressaudit.Observer {
	p.delegate = delegate
	return p
}
func (p *EgressOfferProbe) ObserveEgress(stamp recording.Stamp, a egressaudit.Attempt) recording.OfferResult {
	p.mu.Lock()
	if p.attempts == nil {
		p.attempts = map[string]egressaudit.Attempt{}
	}
	p.attempts[a.ID()] = a
	p.mu.Unlock()
	return p.delegate.ObserveEgress(stamp, a)
}
func (p *EgressOfferProbe) List(context.Context, egressaudit.PageRequest) (egressaudit.Page, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	page := egressaudit.Page{}
	for _, a := range p.attempts {
		page.Items = append(page.Items, egressaudit.Record{Attempt: a})
	}
	return page, nil
}
func WaitRecordingTestRuntime(t *testing.T, r *Runtime) {
	t.Helper()
	waitRecording(t, r)
	waitRecordingMirror(t, r)
}

// Only call after real producers and transport I/O have returned. Shutdown is
// deliberately not a persistence drain and must never use this fixture helper.
func SettledEgressTestPage(t *testing.T, r *Runtime, req egressaudit.PageRequest) (egressaudit.Page, error) {
	t.Helper()
	WaitRecordingTestRuntime(t, r)
	if err := r.recorder.Flush(t.Context()); err != nil {
		return egressaudit.Page{}, err
	}
	return r.EgressAttempts().List(t.Context(), req)
}

func (p *EgressOfferProbe) ScopeStamp(c captureidentity.Reference) recording.Stamp {
	return p.delegate.ScopeStamp(c)
}
