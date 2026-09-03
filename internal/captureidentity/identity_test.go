package captureidentity

import "testing"

func TestReferenceIsTypedAndCanonical(t *testing.T) {
	t.Parallel()
	for _, kind := range []Kind{KindManagedRun, KindManualCapture} {
		reference, err := New(kind, "capture_Abc-123")
		if err != nil || reference.Key() != string(kind)+":capture_Abc-123" {
			t.Fatalf("New(%q) = %+v, %v", kind, reference, err)
		}
	}
	for _, reference := range []Reference{
		{}, {Kind: "other", ID: "capture"}, {Kind: KindManagedRun, ID: ""},
		{Kind: KindManagedRun, ID: " capture"}, {Kind: KindManagedRun, ID: "capture/path"},
	} {
		if err := reference.Validate(); err == nil {
			t.Fatalf("invalid reference accepted: %+v", reference)
		}
	}
}

func TestParseKeyRoundTripsTypedCaptureIdentity(t *testing.T) {
	t.Parallel()

	for _, want := range []Reference{
		{Kind: KindManagedRun, ID: "run-123"},
		{Kind: KindManualCapture, ID: "machine:workspace"},
	} {
		got, err := ParseKey(want.Key())
		if err != nil {
			t.Fatalf("ParseKey(%q): %v", want.Key(), err)
		}
		if got != want {
			t.Fatalf("ParseKey(%q) = %+v, want %+v", want.Key(), got, want)
		}
	}

	for _, value := range []string{"", "managed_run", "other:capture", "managed_run: bad"} {
		if _, err := ParseKey(value); err == nil {
			t.Fatalf("ParseKey(%q) succeeded", value)
		}
	}
}
