package ssewire

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

func Encode(event Event) ([]byte, error) {
	if event.Name == "" {
		return nil, errors.New("SSE event name is empty")
	}
	if strings.ContainsAny(event.Name, "\x00\r\n") {
		return nil, errors.New("SSE event name is invalid")
	}
	if strings.ContainsAny(event.ID, "\x00\r\n") {
		return nil, errors.New("SSE event ID is invalid")
	}
	if !utf8.Valid(event.Data) {
		return nil, errors.New("SSE event data is not valid UTF-8")
	}
	if event.Retry != nil && *event.Retry < 0 {
		return nil, errors.New("SSE retry is negative")
	}

	var encoded bytes.Buffer
	if event.Name != "message" {
		fmt.Fprintf(&encoded, "event: %s\n", event.Name)
	}
	if event.ID != "" {
		fmt.Fprintf(&encoded, "id: %s\n", event.ID)
	}
	if event.Retry != nil {
		fmt.Fprintf(&encoded, "retry: %s\n", strconv.Itoa(*event.Retry))
	}
	for _, line := range bytes.Split(event.Data, []byte{'\n'}) {
		encoded.WriteString("data: ")
		encoded.Write(line)
		encoded.WriteByte('\n')
	}
	encoded.WriteByte('\n')
	return encoded.Bytes(), nil
}
