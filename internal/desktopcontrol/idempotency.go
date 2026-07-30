package desktopcontrol

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
)

const idempotencyCapacity = 512

var errIdempotencyConflict = errors.New("idempotency key was reused for a different command")

type cachedResponse struct {
	status      int
	contentType string
	body        []byte
}

func (response cachedResponse) clone() cachedResponse {
	response.body = append([]byte(nil), response.body...)
	return response
}

type idempotencyEntry struct {
	fingerprint [sha256.Size]byte
	ready       chan struct{}
	response    cachedResponse
}

type idempotencyCache struct {
	mu      sync.Mutex
	entries map[[sha256.Size]byte]*idempotencyEntry
	order   [][sha256.Size]byte
}

func newIdempotencyCache() *idempotencyCache {
	return &idempotencyCache{
		entries: make(map[[sha256.Size]byte]*idempotencyEntry),
	}
}

func (cache *idempotencyCache) execute(
	ctx context.Context,
	key string,
	fingerprint [sha256.Size]byte,
	run func() cachedResponse,
) (cachedResponse, error) {
	keyDigest := sha256.Sum256([]byte("vibermate:control-idempotency:v1:" + key))
	cache.mu.Lock()
	if existing := cache.entries[keyDigest]; existing != nil {
		if existing.fingerprint != fingerprint {
			cache.mu.Unlock()
			return cachedResponse{}, errIdempotencyConflict
		}
		ready := existing.ready
		cache.mu.Unlock()
		select {
		case <-ready:
			cache.mu.Lock()
			response := existing.response.clone()
			cache.mu.Unlock()
			return response, nil
		case <-ctx.Done():
			return cachedResponse{}, ctx.Err()
		}
	}
	entry := &idempotencyEntry{
		fingerprint: fingerprint,
		ready:       make(chan struct{}),
	}
	cache.entries[keyDigest] = entry
	cache.order = append(cache.order, keyDigest)
	cache.evictLocked()
	cache.mu.Unlock()

	response := run().clone()
	cache.mu.Lock()
	entry.response = response.clone()
	close(entry.ready)
	cache.mu.Unlock()
	return response, nil
}

func (cache *idempotencyCache) evictLocked() {
	for len(cache.entries) > idempotencyCapacity && len(cache.order) > 0 {
		oldest := cache.order[0]
		cache.order = cache.order[1:]
		entry := cache.entries[oldest]
		select {
		case <-entry.ready:
			delete(cache.entries, oldest)
		default:
			cache.order = append(cache.order, oldest)
			return
		}
	}
}
