package nativetool

import "testing"

func TestNativeSensitiveCancellationExactLiveTarget(t *testing.T) {
	h, input, observation := sensitiveToolFixture(t)
	defer h.Close()
	target, witness, err := h.BindToolCall(ID("eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"), input, observation)
	if err != nil {
		t.Fatal(err)
	}
	cancel := input
	cancel.Kind = RequestCancel
	cancel.Intents = nil
	cancel.ObservedCallID = nil
	cancel.CatalogEvidence = nil
	cancel.ExactRPC = []byte(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`)
	binding, err := h.BindCancellation(cancel, target)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := target.RPCIDDigest()
	got, ok := binding.CancelledRPCIDDigest()
	if !ok || got != want {
		t.Fatal("cancel identity did not use target MAC")
	}
	wrong := cancel
	wrong.ExactRPC = []byte(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":2}}`)
	if _, err = h.BindCancellation(wrong, target); err == nil {
		t.Fatal("substituted target accepted")
	}
	wrong = cancel
	wrong.SessionID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, err = h.BindCancellation(wrong, target); err == nil {
		t.Fatal("foreign session accepted")
	}
	other, _, _ := sensitiveToolFixture(t)
	defer other.Close()
	if _, err = other.BindCancellation(cancel, target); err == nil {
		t.Fatal("foreign hasher accepted")
	}
	if err = h.RetireToolCall(witness); err != nil {
		t.Fatal(err)
	}
	if _, err = h.BindCancellation(cancel, target); err == nil {
		t.Fatal("retired target accepted")
	}
}
