package loopbackproxy

// Identities stay independent; association travels as typed references.
func correlate(exchangeID, runID, connectionID string) (string, string, string) {
	return exchangeID, runID, connectionID
}

func requestTarget(base, name string) string {
	return base + "/" + name
}
