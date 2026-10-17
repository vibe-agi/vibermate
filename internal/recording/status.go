// Package recording provides bounded ownership for optional observations.
// It performs no I/O and grants no execution authority.
package recording

type Kind uint8

const (
	Connection Kind = iota
	Egress
	Activity
	Usage
	Content
	Raw
	Native
	ACP
	Capture
	KindCount
)

type OfferResult uint8

// Accepted denotes in-memory ownership, never durable confirmation.
const (
	Accepted OfferResult = iota
	DroppedCapacity
	DroppedUnavailable
	DroppedTooLarge
	DroppedInvalid
	DroppedStale
	DroppedClosed
	DroppedRolledBack
	DroppedControlCommitUnknown
	DroppedOwnerCanceled
	OfferResultCount
)

type Settlement uint8

const (
	Persisted Settlement = iota
	Discarded
	Unconfirmed
	SettlementCount
)

type Count struct{ Records, Bytes uint64 }
type KindStatistics struct {
	Offers      [OfferResultCount]Count
	Settlements [SettlementCount]Count
}
type Statistics struct {
	ReservedRecords, ReservedBytes int64
	ByKind                         [KindCount]KindStatistics
}
