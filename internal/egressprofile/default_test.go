package egressprofile

import (
	"context"
	"errors"
	"testing"
)

type unavailableDefault struct{}

func (unavailableDefault) LoadDefault(context.Context) (DefaultSetting, error) {
	return DefaultSetting{}, ErrDefaultUnavailable
}
func (unavailableDefault) WriteDefault(context.Context, uint64, DefaultSetting) (DefaultCommitResult, error) {
	return DefaultCommitResult{}, ErrDefaultUnavailable
}

// A bad global dependency must not obstruct higher explicit choices, but must
// never become absence/direct when the consumer genuinely needs inheritance.
func TestDefaultResolverExplicitPrecedenceDoesNotReadUnavailableGlobal(t *testing.T) {
	m := &DefaultManager{repository: unavailableDefault{}}
	direct, err := Explicit(Direct())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		account, flow Choice
		source        SelectionSource
	}{{direct, Inherit(), SelectionAccount}, {Inherit(), direct, SelectionFlow}, {direct, direct, SelectionAccount}} {
		got, err := m.Resolve(context.Background(), test.account, test.flow)
		if err != nil || got.Source() != test.source || got.DefaultRevision() != 0 || !got.Profile().Equal(Direct()) {
			t.Fatalf("explicit result %+v %v", got, err)
		}
	}
	if _, err = m.Resolve(context.Background(), Inherit(), Inherit()); !errors.Is(err, ErrDefaultUnavailable) {
		t.Fatalf("global error became absence: %v", err)
	}
	if _, err = m.Resolve(context.Background(), direct, Choice{}); err == nil {
		t.Fatal("invalid lower choice accepted")
	}
}

func TestDefaultResolverCancellationAndNestedNullFailClosed(t *testing.T) {
	direct, _ := Explicit(Direct())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := &DefaultManager{repository: unavailableDefault{}}
	if _, err := m.Resolve(ctx, direct, Inherit()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation hidden: %v", err)
	}
}
