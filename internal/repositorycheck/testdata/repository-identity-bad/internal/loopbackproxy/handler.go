package loopbackproxy

import "strings"

// Encoding containment in an identity string is what ADR-0015 forbids.
func correlate(exchangeID, runID string) string {
	return runID + "/" + exchangeID
}

func belongs(requestID, actionID string) bool {
	return strings.HasPrefix(requestID, actionID+"/")
}
