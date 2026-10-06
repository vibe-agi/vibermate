package runtimepersistence

import (
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"testing"
)

func scanStoredJSON(s *storedJSONScanner, b []byte) bool {
	s.reset()
	for _, c := range b {
		s.bytes++
		s.step(s, c)
		if s.err != nil {
			return false
		}
	}
	return s.eof() != scanError && s.err == nil
}

func TestStoredJSONScannerGrammarAndReuse(t *testing.T) {
	cases := []string{`null`, `true`, `false`, `0`, `-0`, `1e+02`, `1e9999999999999999999999`, `[]`, `{}`, `{"a":[1,{"a":2,"a":3}],"b":"\ud800"}`, "\"\xff\"", `[1,]`, `{"a":}`, `"\'"`, `01`, `1e+`, `trueX`, `{}[]`, `[}`, `"\u00zz"`}
	s := &storedJSONScanner{}
	for _, data := range cases {
		for end := 0; end <= len(data); end++ {
			b := []byte(data[:end])
			if got, want := scanStoredJSON(s, b), json.Valid(b); got != want {
				t.Fatalf("%q: got %v want %v", b, got, want)
			}
		}
	}
	for _, depth := range []int{9999, 10000, 10001} {
		b := []byte(strings.Repeat("[", depth) + "0" + strings.Repeat("]", depth))
		if got, want := scanStoredJSON(s, b), json.Valid(b); got != want {
			t.Fatalf("depth %d: %v != %v", depth, got, want)
		}
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range 10000 {
		if !scanStoredJSON(s, []byte(`{"x":null}`)) {
			t.Fatal("shallow reuse")
		}
	}
	runtime.ReadMemStats(&after)
	if n := after.TotalAlloc - before.TotalAlloc; n > 16<<10 {
		t.Fatalf("reused scanner allocated %d", n)
	}
	t.Logf("10000 shallow values after max depth: %d allocated bytes", after.TotalAlloc-before.TotalAlloc)
	fresh := &storedJSONScanner{}
	runtime.ReadMemStats(&before)
	for range 10000 {
		if !scanStoredJSON(fresh, []byte(`null`)) {
			t.Fatal("tiny")
		}
	}
	runtime.ReadMemStats(&after)
	if n := after.TotalAlloc - before.TotalAlloc; n > 16<<10 {
		t.Fatalf("tiny scanner allocated %d", n)
	}
}

func TestStoredJSONScannerReservationBeforeGrowth(t *testing.T) {
	denied := errors.New("stack denied")
	s := &storedJSONScanner{}
	s.grow = func(old, next int) error {
		if cap(s.parseState) != old || next <= old {
			t.Fatal("growth already occurred")
		}
		return denied
	}
	if scanStoredJSON(s, []byte(strings.Repeat("[", 65)+"0"+strings.Repeat("]", 65))) || s.err != denied || cap(s.parseState) != 64 {
		t.Fatalf("reservation: %v cap=%d", s.err, cap(s.parseState))
	}
}

func FuzzStoredJSONScanner(f *testing.F) {
	for _, s := range []string{`null`, `{"x":"\ud800","x":[1e+02]}`, "\"\xff\"", `[1,]`, `01`, `{}[]`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 64<<10 {
			t.Skip()
		}
		s := &storedJSONScanner{}
		if got, want := scanStoredJSON(s, b), json.Valid(b); got != want {
			t.Fatalf("grammar got %v want %v: %q", got, want, b)
		}
	})
}
