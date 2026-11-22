package openairesponses

import "time"

// SetProviderSideClock replaces the clock a provider side uses for reasoning
// heartbeats and retry dates, so tests can step time.
func SetProviderSideClock(side *ProviderSide, now func() time.Time) {
	side.now = now
}

// SetProviderSideMessageID replaces the provisional message id source, so
// streams that begin without response.created give stable bytes.
func SetProviderSideMessageID(side *ProviderSide, messageID func() string) {
	side.messageID = messageID
}
