package nativemcp

import (
	"bytes"
	"strings"
	"testing"
)

func TestNativeStrictRPCRejectsMalformedBeforeOutbound(t *testing.T) {
	for _, raw := range []string{
		`{"jsonrpc":"2.0","id":1,"id":2,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"a","arguments":{"x":1,"x":2}}}`,
		`{"jsonrpc":"2.0","id":1,"method":"ping"} {}`,
		`[{"jsonrpc":"2.0","id":1,"method":"ping"}]`,
		`{"jsonrpc":"2.0","id":null,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":true,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":1,"method":"ping","result":{}}`,
		`{"jsonrpc":"2.0","id":1,"method":"server/discover"}`,
		"{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\",\"_meta\":{\"x\":\"\xff\"}}",
	} {
		if _, err := parseRequest([]byte(raw), DefaultLimits()); err == nil {
			t.Errorf("accepted malformed RPC %q", raw)
		}
	}
	if _, err := parseRequest([]byte(`{"jsonrpc":"2.0","id":"1","method":"ping"}`), DefaultLimits()); err != nil {
		t.Fatal(err)
	}
}

func TestNativeLimitsExactBoundary(t *testing.T) {
	l := DefaultLimits()
	for _, boundary := range []int64{l.RequestBytes, l.ResultBytes, l.EventBytes, l.CatalogPageBytes, l.CatalogBytes, l.BufferBytes} {
		budget := memoryBudget{maximum: boundary}
		if !budget.acquire(boundary) || budget.acquire(1) {
			t.Fatal("budget boundary not enforced")
		}
		budget.release(boundary)
		if !budget.acquire(1) {
			t.Fatal("released budget unavailable")
		}
	}
	for _, depth := range []int{64, 65} {
		raw := []byte(strings.Repeat(`{"x":`, depth) + `0` + strings.Repeat(`}`, depth))
		_, err := decodeObject(raw, l.ResultBytes, l.Depth)
		if (err == nil) != (depth == 64) {
			t.Fatalf("depth %d: %v", depth, err)
		}
	}
	for _, size := range []int{512, 513} {
		raw := []byte(`{"jsonrpc":"2.0","id":"` + strings.Repeat("x", size) + `","method":"ping"}`)
		_, err := parseRequest(raw, l)
		if (err == nil) != (size == 512) {
			t.Fatalf("id %d: %v", size, err)
		}
	}
	base := []byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	for _, delta := range []int64{0, 1} {
		raw := append(bytes.Clone(base), bytes.Repeat([]byte(" "), int(l.RequestBytes-int64(len(base))+delta))...)
		_, err := parseRequest(raw, l)
		if (err == nil) != (delta == 0) {
			t.Fatalf("request boundary %d: %v", delta, err)
		}
	}
}

func TestNativeLargeResultDecoderSeparateFromBindingLimit(t *testing.T) {
	for _, size := range []int{2 << 20, (2 << 20) + 1, 16 << 20, (16 << 20) + 1} {
		base := []byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":""}]}}`)
		raw := []byte(strings.Replace(string(base), `"text":""`, `"text":"`+strings.Repeat("x", size-len(base))+`"`, 1))
		_, err := parseResponse(raw, DefaultLimits())
		if (err == nil) != (size <= 16<<20) {
			t.Fatalf("result %d: %v", size, err)
		}
	}
}
