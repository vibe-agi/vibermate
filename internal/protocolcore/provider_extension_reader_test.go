package protocolcore

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestProviderExtensionReaderIsOrderedImmutableAndExpires(t *testing.T) {
	fragments := [][]byte{[]byte(`"a"`), []byte(`{"x":1}`)}
	ext, err := NewProviderExtension(ProviderExtensionSourceOpenAIResponses, ProviderExtensionReasoningContent, "path", fragments)
	if err != nil {
		t.Fatal(err)
	}
	var saved io.Reader
	count := 0
	err = ext.WalkFragments(func(size int, r io.Reader) error {
		if _, ok := r.(io.WriterTo); ok {
			t.Fatal("reader exposes an alternate access surface")
		}
		saved = r
		var got bytes.Buffer
		var scratch [2]byte
		for {
			n, err := r.Read(scratch[:])
			got.Write(scratch[:n])
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			for i := range scratch {
				scratch[i] = 'x'
			}
		}
		if size != len(fragments[count]) || !bytes.Equal(got.Bytes(), fragments[count]) {
			t.Fatal("fragment order/bytes changed")
		}
		count++
		return nil
	})
	if err != nil || count != 2 {
		t.Fatalf("walk %d %v", count, err)
	}
	if _, err := saved.Read(make([]byte, 1)); err == nil {
		t.Fatal("retained reader remained usable")
	}
	if !bytes.Equal(ext.Fragments()[1], fragments[1]) {
		t.Fatal("input changed")
	}
	sentinel := errors.New("stop")
	count = 0
	if err := ext.WalkFragments(func(int, io.Reader) error { count++; return sentinel }); err != sentinel || count != 1 {
		t.Fatal("callback did not stop walk")
	}
	if ext.WalkFragments(nil) == nil {
		t.Fatal("nil callback accepted")
	}
}
