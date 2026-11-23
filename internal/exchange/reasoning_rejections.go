package exchange

import (
	"container/list"
	"sync"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

// maxReasoningRejections bounds the rejected-seal memory (design 8.6).
const maxReasoningRejections = 4096

// reasoningRejections remembers reasoning seals an upstream refused as
// invalid reasoning state. When a translated stream fails in band, the
// client cannot heal itself by stripping its thinking blocks, so it replays
// the same seals on the next turn; the provider side consults this memory
// through ProviderContext.RejectedSeal and drops them instead of failing that
// turn too. The memory holds digests only, is bounded, least recently used
// first out, and is lost on restart, which costs at most one more failed
// turn.
type reasoningRejections struct {
	mu      sync.Mutex
	limit   int
	order   *list.List // of [32]byte, most recently used at the front
	entries map[[32]byte]*list.Element
}

func newReasoningRejections(limit int) *reasoningRejections {
	return &reasoningRejections{
		limit:   limit,
		order:   list.New(),
		entries: make(map[[32]byte]*list.Element),
	}
}

// contains reports whether digest was rejected, and counts the lookup as a
// use: a seal the conversation keeps replaying stays remembered.
func (memory *reasoningRejections) contains(digest [32]byte) bool {
	if memory == nil {
		return false
	}
	memory.mu.Lock()
	defer memory.mu.Unlock()
	element, found := memory.entries[digest]
	if found {
		memory.order.MoveToFront(element)
	}
	return found
}

func (memory *reasoningRejections) record(digest [32]byte) {
	if memory == nil || memory.limit <= 0 {
		return
	}
	memory.mu.Lock()
	defer memory.mu.Unlock()
	if element, found := memory.entries[digest]; found {
		memory.order.MoveToFront(element)
		return
	}
	memory.entries[digest] = memory.order.PushFront(digest)
	for memory.order.Len() > memory.limit {
		oldest := memory.order.Back()
		delete(memory.entries, oldest.Value.([32]byte))
		memory.order.Remove(oldest)
	}
}

// recordRequest remembers every OpenAI Responses seal the failed request
// held. Only that dialect's seals can be replayed to a Responses upstream,
// so only they can have caused the rejection.
func (memory *reasoningRejections) recordRequest(request protocolcore.Request) {
	for _, message := range request.Messages {
		for _, block := range message.Blocks {
			if block.Kind != protocolcore.BlockReasoning || block.Reasoning == nil ||
				block.Reasoning.Seal.Format != protocolcore.SealResponsesEncrypted {
				continue
			}
			memory.record(protocolcore.SealDigest(block.Reasoning.Seal))
		}
	}
}

func (memory *reasoningRejections) size() int {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	return memory.order.Len()
}
