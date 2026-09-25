package productruntime

import "testing"

func TestStorageCapacityStateUsesThePublishedThreshold(t *testing.T) {
	below := storageLowSpaceThresholdBytes - 1
	at := storageLowSpaceThresholdBytes
	if storageCapacityState(nil) != "unavailable" ||
		storageCapacityState(&below) != "low" ||
		storageCapacityState(&at) != "healthy" {
		t.Fatal("storage capacity threshold classification changed")
	}
}
