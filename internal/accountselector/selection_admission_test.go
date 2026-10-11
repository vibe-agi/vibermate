package accountselector

import (
	"errors"
	"runtime"
	"testing"

	"github.com/dop251/goja"
)

func TestSelectorPrimitiveAdmissionBoundsConversionAndMembership(t *testing.T) {
	vm := goja.New()
	large, err := vm.RunString("\"\u03bb\".repeat(1024*1024)")
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]struct{}{"account.allowed": {}, "\u03bb\U0001f642": {}}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err = selectedAccountIDWithin(large, allowed)
	runtime.ReadMemStats(&after)
	if !errors.Is(err, ErrInvalidSelection) {
		t.Fatalf("large selected identity: %v", err)
	}
	if n := after.TotalAlloc - before.TotalAlloc; n > 1<<20 {
		t.Fatalf("selected string converted before admission: %d", n)
	}
	for _, id := range []string{"account.allowed", "\u03bb\U0001f642"} {
		got, err := selectedAccountIDWithin(vm.ToValue(id), allowed)
		if err != nil || got != id {
			t.Fatalf("valid identity %q: %q %v", id, got, err)
		}
	}
	for _, value := range []goja.Value{vm.ToValue("foreign"), vm.ToValue(123), goja.Null()} {
		if _, err := selectedAccountIDWithin(value, allowed); !errors.Is(err, ErrInvalidSelection) {
			t.Fatalf("invalid selection accepted: %v", err)
		}
	}
}
