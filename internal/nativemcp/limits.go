package nativemcp

import (
	"errors"
	"sync"
	"time"
)

var errProtocol = errors.New("native protocol invalid")
var errBudget = errors.New("native budget exceeded")
var errSession = errors.New("native session invalid")

type Limits struct {
	RequestBytes, ResultBytes, EventBytes, CatalogPageBytes, CatalogBytes, BufferBytes                      int64
	Depth, OpaqueIDBytes, CatalogTools, CatalogPages, SessionsPerCapture, ConcurrentRPCsPerSession, Pending int
	SessionIdle, Headers, StreamIdle, CallLifetime                                                          time.Duration
}

// DefaultLimits are controller-adopted safety ceilings, not measured client support.
func DefaultLimits() Limits {
	return Limits{1 << 20, 16 << 20, 16 << 20, 1 << 20, 8 << 20, 64 << 20, 64, 512, 1024, 32, 64, 16, 1024, 30 * time.Minute, 30 * time.Second, 5 * time.Minute, 30 * time.Minute}
}
func (l Limits) validate() error {
	d := DefaultLimits()
	for _, pair := range [][2]int64{{l.RequestBytes, d.RequestBytes}, {l.ResultBytes, d.ResultBytes}, {l.EventBytes, d.EventBytes}, {l.CatalogPageBytes, d.CatalogPageBytes}, {l.CatalogBytes, d.CatalogBytes}, {l.BufferBytes, d.BufferBytes}, {int64(l.Depth), int64(d.Depth)}, {int64(l.OpaqueIDBytes), int64(d.OpaqueIDBytes)}, {int64(l.CatalogTools), int64(d.CatalogTools)}, {int64(l.CatalogPages), int64(d.CatalogPages)}, {int64(l.SessionsPerCapture), int64(d.SessionsPerCapture)}, {int64(l.ConcurrentRPCsPerSession), int64(d.ConcurrentRPCsPerSession)}, {int64(l.Pending), int64(d.Pending)}, {int64(l.SessionIdle), int64(d.SessionIdle)}, {int64(l.Headers), int64(d.Headers)}, {int64(l.StreamIdle), int64(d.StreamIdle)}, {int64(l.CallLifetime), int64(d.CallLifetime)}} {
		if pair[0] <= 0 || pair[0] > pair[1] {
			return errBudget
		}
	}
	return nil
}

type memoryBudget struct {
	mu            sync.Mutex
	maximum, used int64
}

func (b *memoryBudget) acquire(n int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n < 0 || n > b.maximum-b.used {
		return false
	}
	b.used += n
	return true
}
func (b *memoryBudget) release(n int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.used -= n
	if b.used < 0 {
		panic("native budget released twice")
	}
}
