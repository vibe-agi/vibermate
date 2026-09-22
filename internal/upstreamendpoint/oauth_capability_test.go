package upstreamendpoint

import (
	"context"
	"slices"
	"testing"

	"github.com/vibe-agi/vibermate/internal/providerauth"
)

func TestRecoveredChatGPTServicesGainOAuthWithoutLosingIdentityOrStaticCredentials(t *testing.T) {
	ctx := context.Background()
	repository := newMemoryEndpointRepository()
	builtins, err := BuiltInCommands()
	if err != nil {
		t.Fatal(err)
	}
	for index := range builtins {
		if builtins[index].ID == ChatGPTOfficialID {
			builtins[index].Drivers = []providerauth.DriverRef{providerauth.StaticHeaderDriverRef()}
		}
	}
	initial, err := NewManager(ctx, repository, builtins, fixedClock{})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := initial.Get(ctx, ChatGPTOfficialID)
	_ = initial.Shutdown(ctx)
	current, err := NewManager(ctx, repository, builtins, fixedClock{})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := current.Get(ctx, ChatGPTOfficialID)
	if got.Revision != before.Revision+1 || got.Origin != before.Origin || got.RealmID != before.RealmID || got.DisplayName != before.DisplayName ||
		!slices.Contains(got.Drivers, providerauth.CodexOAuthDriverRef()) || !slices.Contains(got.Drivers, providerauth.StaticHeaderDriverRef()) {
		t.Fatal("recovered ChatGPT service lacks the current auth capability")
	}
	other, _ := current.Get(ctx, OpenAIPlatformID)
	if slices.Contains(other.Drivers, providerauth.CodexOAuthDriverRef()) || other.Revision != 1 {
		t.Fatal("OAuth capability expanded to an unrelated origin")
	}
	_ = current.Shutdown(ctx)
	reopened, err := NewManager(ctx, repository, builtins, fixedClock{})
	if err != nil {
		t.Fatal(err)
	}
	again, _ := reopened.Get(ctx, ChatGPTOfficialID)
	if !again.Equal(got) {
		t.Fatal("capability repair repeats on every startup")
	}
}
