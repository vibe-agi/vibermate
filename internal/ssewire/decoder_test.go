package ssewire

import (
	"bytes"
	"errors"
	"testing"
)

func TestDecoderAcceptsFragmentedCRLFMultilineEvents(t *testing.T) {
	t.Parallel()

	decoder, err := NewDecoder(DefaultOptions())
	if err != nil {
		t.Fatalf("NewDecoder() error = %v", err)
	}
	wire := []byte(": ping\r\nevent: update\r\nid: 7\r\ndata: first\r\ndata: second\r\n\r\n")
	var events []Event
	for _, fragment := range wire {
		produced, feedErr := decoder.Feed([]byte{fragment})
		if feedErr != nil {
			t.Fatalf("Feed() error = %v", feedErr)
		}
		events = append(events, produced...)
	}
	if err := decoder.Finish(); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1", len(events))
	}
	if events[0].Name != "update" ||
		events[0].ID != "7" ||
		string(events[0].Data) != "first\nsecond" {
		t.Fatalf("event = %#v", events[0])
	}
}

func TestDecoderRejectsPartialEventAtEOF(t *testing.T) {
	t.Parallel()

	decoder, err := NewDecoder(DefaultOptions())
	if err != nil {
		t.Fatalf("NewDecoder() error = %v", err)
	}
	if _, err := decoder.Feed([]byte("data: partial\n")); err != nil {
		t.Fatalf("Feed() error = %v", err)
	}
	if err := decoder.Finish(); !errors.Is(err, ErrTruncated) {
		t.Fatalf("Finish() error = %v, want ErrTruncated", err)
	}
}

func TestDecoderEnforcesLineEventAndCountLimits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		options Options
		wire    []byte
	}{
		{
			name: "line",
			options: Options{
				MaxLineBytes:    4,
				MaxEventBytes:   32,
				MaxPendingBytes: 32,
				MaxEvents:       2,
			},
			wire: []byte("data: value\n\n"),
		},
		{
			name: "event",
			options: Options{
				MaxLineBytes:    32,
				MaxEventBytes:   2,
				MaxPendingBytes: 32,
				MaxEvents:       2,
			},
			wire: []byte("data: value\n\n"),
		},
		{
			name: "count",
			options: Options{
				MaxLineBytes:    32,
				MaxEventBytes:   32,
				MaxPendingBytes: 64,
				MaxEvents:       1,
			},
			wire: []byte("data: one\n\ndata: two\n\n"),
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			decoder, err := NewDecoder(test.options)
			if err != nil {
				t.Fatalf("NewDecoder() error = %v", err)
			}
			if _, err := decoder.Feed(test.wire); !errors.Is(err, ErrLimitExceeded) {
				t.Fatalf("Feed() error = %v, want ErrLimitExceeded", err)
			}
		})
	}
}

func TestEncodeRoundTripsEvent(t *testing.T) {
	t.Parallel()

	retry := 1200
	encoded, err := Encode(Event{
		Name:  "update",
		Data:  []byte("one\ntwo"),
		ID:    "event-1",
		Retry: &retry,
	})
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	decoder, err := NewDecoder(DefaultOptions())
	if err != nil {
		t.Fatalf("NewDecoder() error = %v", err)
	}
	events, err := decoder.Feed(encoded)
	if err != nil {
		t.Fatalf("Feed() error = %v", err)
	}
	if err := decoder.Finish(); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	if len(events) != 1 ||
		events[0].Name != "update" ||
		events[0].ID != "event-1" ||
		!bytes.Equal(events[0].Data, []byte("one\ntwo")) ||
		events[0].Retry == nil ||
		*events[0].Retry != retry {
		t.Fatalf("round-trip event = %#v", events)
	}
}

func FuzzDecoderFragmentation(f *testing.F) {
	f.Add([]byte("event: message\ndata: {\"value\":1}\n\n"))
	f.Add([]byte(": ping\r\ndata: [DONE]\r\n\r\n"))
	f.Fuzz(func(t *testing.T, wire []byte) {
		options := DefaultOptions()
		options.MaxPendingBytes = 64 << 10
		options.MaxLineBytes = 16 << 10
		options.MaxEventBytes = 32 << 10
		options.MaxEvents = 1024
		decoder, err := NewDecoder(options)
		if err != nil {
			t.Fatal(err)
		}
		for offset := 0; offset < len(wire); {
			size := 1 + offset%17
			end := min(offset+size, len(wire))
			if _, err := decoder.Feed(wire[offset:end]); err != nil {
				return
			}
			offset = end
		}
		_ = decoder.Finish()
	})
}
