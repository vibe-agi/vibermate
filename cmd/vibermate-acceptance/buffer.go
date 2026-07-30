package main

import (
	"bytes"
	"sync"
)

type boundedBuffer struct {
	mu       sync.Mutex
	limit    int
	buffer   bytes.Buffer
	overflow bool
}

func newBoundedBuffer(limit int) *boundedBuffer {
	return &boundedBuffer{limit: limit}
}

func (buffer *boundedBuffer) Write(value []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	accepted := len(value)
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining <= 0 {
		buffer.overflow = true
		return accepted, nil
	}
	if len(value) > remaining {
		value = value[:remaining]
		buffer.overflow = true
	}
	_, _ = buffer.buffer.Write(value)
	return accepted, nil
}

func (buffer *boundedBuffer) Len() int {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.Len()
}

func (buffer *boundedBuffer) snapshot() ([]byte, bool) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return bytes.Clone(buffer.buffer.Bytes()), buffer.overflow
}
