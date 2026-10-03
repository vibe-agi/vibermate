package egressaudit

import (
	"github.com/vibe-agi/vibermate/internal/environment"
	"testing"
)

func TestNativePurposeCatalogUsesValidNativeAttempt(t *testing.T) {
	if _, err := NewClientToolAttempt(environment.ClientToolPlan{}, NewInput{}); err == nil {
		t.Fatal("invalid native plan accepted")
	}
	authority, err := AuthorityForPurpose(PurposeClientToolService)
	if err != nil || authority != AuthorityEnvironment {
		t.Fatal("native purpose has no environment authority")
	}
}
