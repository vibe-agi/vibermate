package resourcedeletion

import (
	"errors"
	"strings"
	"testing"
)

func validHolder() Holder {
	return Holder{
		Kind:   KindRunningCapture,
		ID:     "run.abc",
		Label:  "claude on agent-lab",
		Detail: "attached",
	}
}

// The two answers are mutually exclusive by construction. A refusal with no
// holders would tell a user their delete failed and give them nothing to act
// on, which is the failure this package exists to make unrepresentable.
func TestARefusalMustNameAtLeastOneHolder(t *testing.T) {
	t.Parallel()
	if _, err := Refused(nil); !errors.Is(err, ErrInvalidHolder) {
		t.Fatal("a refusal with no holders was accepted")
	}
	if got := Completed(); !got.Deleted || len(got.Holders) != 0 {
		t.Fatalf("Completed() = %+v", got)
	}
}

func TestAHolderMustBeBothRecognisableAndActionable(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(Holder) Holder{
		"no kind":      func(h Holder) Holder { h.Kind = ""; return h },
		"unknown kind": func(h Holder) Holder { h.Kind = "invented"; return h },
		"no id":        func(h Holder) Holder { h.ID = ""; return h },
		"blank id":     func(h Holder) Holder { h.ID = "   "; return h },
		"no label":     func(h Holder) Holder { h.Label = ""; return h },
		"long id":      func(h Holder) Holder { h.ID = strings.Repeat("a", 257); return h },
		"long detail":  func(h Holder) Holder { h.Detail = strings.Repeat("a", 513); return h },
	} {
		if _, err := Refused([]Holder{mutate(validHolder())}); !errors.Is(
			err, ErrInvalidHolder,
		) {
			t.Fatalf("%s was accepted as a holder", name)
		}
	}
	if err := validHolder().Validate(); err != nil {
		t.Fatalf("a complete holder was rejected: %v", err)
	}
}

// The order a user reads holders in must not depend on which lookup happened to
// answer first, or the same refusal reads differently on every attempt.
func TestHoldersReadInAStableOrder(t *testing.T) {
	t.Parallel()
	holders := []Holder{
		{Kind: KindWorkspaceDefault, ID: "w2", Label: "second workspace"},
		{Kind: KindRunningCapture, ID: "r2", Label: "second run"},
		{Kind: KindWorkspaceDefault, ID: "w1", Label: "first workspace"},
		{Kind: KindRunningCapture, ID: "r1", Label: "first run"},
	}
	first, err := Refused(holders)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Refused([]Holder{holders[3], holders[0], holders[1], holders[2]})
	if err != nil {
		t.Fatal(err)
	}
	for index := range first.Holders {
		if first.Holders[index].ID != second.Holders[index].ID {
			t.Fatalf("order depends on input: %+v vs %+v", first, second)
		}
	}
	if first.Holders[0].ID != "r1" || first.Holders[3].ID != "w2" {
		t.Fatalf("unexpected order: %+v", first.Holders)
	}
	if first.Deleted {
		t.Fatal("a refusal reported itself as deleted")
	}
}
