package ssewire

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

var (
	ErrMalformed     = errors.New("SSE framing is malformed")
	ErrLimitExceeded = errors.New("SSE framing limit was exceeded")
	ErrTruncated     = errors.New("SSE event stream ended with a partial event")
	ErrClosed        = errors.New("SSE decoder is closed")
)

type Options struct {
	MaxLineBytes    int
	MaxEventBytes   int
	MaxPendingBytes int
	MaxEvents       int
}

func DefaultOptions() Options {
	return Options{
		MaxLineBytes:    1 << 20,
		MaxEventBytes:   4 << 20,
		MaxPendingBytes: 4 << 20,
		MaxEvents:       1 << 20,
	}
}

func (options Options) validate() error {
	if options.MaxLineBytes <= 0 ||
		options.MaxEventBytes <= 0 ||
		options.MaxPendingBytes <= 0 ||
		options.MaxEvents <= 0 {
		return errors.New("SSE limits must be positive")
	}
	if options.MaxPendingBytes < options.MaxLineBytes {
		return errors.New("SSE pending byte limit is smaller than the line limit")
	}
	return nil
}

type Event struct {
	Name  string
	Data  []byte
	ID    string
	Retry *int
}

func (event Event) Clone() Event {
	cloned := event
	cloned.Data = bytes.Clone(event.Data)
	if event.Retry != nil {
		value := *event.Retry
		cloned.Retry = &value
	}
	return cloned
}

type Decoder struct {
	mu sync.Mutex

	options Options
	pending []byte
	// A CR is itself an SSE line ending. When it arrives at the end of one
	// fragment, consume it immediately and ignore one LF at the beginning of
	// the next fragment so a fragmented CRLF remains one line ending.
	swallowLeadingLF bool

	eventName  string
	dataLines  []string
	eventBytes int
	lastID     string
	retry      *int
	eventCount int
	closed     bool
}

func NewDecoder(options Options) (*Decoder, error) {
	if err := options.validate(); err != nil {
		return nil, err
	}
	return &Decoder{options: options}, nil
}

// Feed accepts arbitrarily fragmented bytes and dispatches only blank-line
// terminated events.
func (decoder *Decoder) Feed(fragment []byte) ([]Event, error) {
	decoder.mu.Lock()
	defer decoder.mu.Unlock()

	if decoder.closed {
		return nil, ErrClosed
	}
	if len(fragment) == 0 {
		return nil, nil
	}
	if decoder.swallowLeadingLF {
		if fragment[0] == '\n' {
			fragment = fragment[1:]
		}
		decoder.swallowLeadingLF = false
		if len(fragment) == 0 {
			return nil, nil
		}
	}
	if len(decoder.pending)+len(fragment) > decoder.options.MaxPendingBytes {
		return nil, fmt.Errorf("%w: pending bytes", ErrLimitExceeded)
	}
	decoder.pending = append(decoder.pending, fragment...)

	var events []Event
	for {
		newline := bytes.IndexAny(decoder.pending, "\r\n")
		if newline < 0 {
			if len(decoder.pending) > decoder.options.MaxLineBytes {
				return nil, fmt.Errorf("%w: line bytes", ErrLimitExceeded)
			}
			break
		}

		line := decoder.pending[:newline]
		terminatorBytes := 1
		if decoder.pending[newline] == '\r' {
			if newline+1 < len(decoder.pending) && decoder.pending[newline+1] == '\n' {
				terminatorBytes = 2
			} else if newline+1 == len(decoder.pending) {
				decoder.swallowLeadingLF = true
			}
		}
		decoder.pending = decoder.pending[newline+terminatorBytes:]
		if len(line) > decoder.options.MaxLineBytes {
			return nil, fmt.Errorf("%w: line bytes", ErrLimitExceeded)
		}
		if !utf8.Valid(line) {
			return nil, fmt.Errorf("%w: line is not valid UTF-8", ErrMalformed)
		}

		event, dispatched, err := decoder.consumeLine(string(line))
		if err != nil {
			return nil, err
		}
		if dispatched {
			events = append(events, event)
		}
	}
	return events, nil
}

func (decoder *Decoder) Finish() error {
	decoder.mu.Lock()
	defer decoder.mu.Unlock()

	if decoder.closed {
		return ErrClosed
	}
	decoder.closed = true
	if len(decoder.pending) != 0 ||
		len(decoder.dataLines) != 0 ||
		decoder.eventName != "" ||
		decoder.retry != nil {
		return ErrTruncated
	}
	return nil
}

func (decoder *Decoder) consumeLine(line string) (Event, bool, error) {
	if line == "" {
		return decoder.dispatch()
	}
	if strings.HasPrefix(line, ":") {
		return Event{}, false, nil
	}

	field, value, found := strings.Cut(line, ":")
	if !found {
		value = ""
	} else if strings.HasPrefix(value, " ") {
		value = value[1:]
	}

	switch field {
	case "data":
		decoder.eventBytes += len(value)
		if len(decoder.dataLines) > 0 {
			decoder.eventBytes++
		}
		if decoder.eventBytes > decoder.options.MaxEventBytes {
			return Event{}, false, fmt.Errorf("%w: event data bytes", ErrLimitExceeded)
		}
		decoder.dataLines = append(decoder.dataLines, value)
	case "event":
		if strings.ContainsAny(value, "\x00\r\n") {
			return Event{}, false, fmt.Errorf("%w: event name", ErrMalformed)
		}
		decoder.eventName = value
	case "id":
		if strings.ContainsAny(value, "\x00\r\n") {
			return Event{}, false, fmt.Errorf("%w: event ID", ErrMalformed)
		}
		decoder.lastID = value
	case "retry":
		if value == "" {
			return Event{}, false, fmt.Errorf("%w: empty retry", ErrMalformed)
		}
		for _, character := range value {
			if character < '0' || character > '9' {
				return Event{}, false, fmt.Errorf("%w: retry is not an integer", ErrMalformed)
			}
		}
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return Event{}, false, fmt.Errorf("%w: retry value: %v", ErrMalformed, err)
		}
		decoder.retry = &parsed
	default:
		// Unknown fields are ignored by the SSE wire specification.
	}
	return Event{}, false, nil
}

func (decoder *Decoder) dispatch() (Event, bool, error) {
	if len(decoder.dataLines) == 0 {
		decoder.eventName = ""
		decoder.eventBytes = 0
		decoder.retry = nil
		return Event{}, false, nil
	}
	decoder.eventCount++
	if decoder.eventCount > decoder.options.MaxEvents {
		return Event{}, false, fmt.Errorf("%w: event count", ErrLimitExceeded)
	}
	name := decoder.eventName
	if name == "" {
		name = "message"
	}
	event := Event{
		Name:  name,
		Data:  []byte(strings.Join(decoder.dataLines, "\n")),
		ID:    decoder.lastID,
		Retry: decoder.retry,
	}
	decoder.eventName = ""
	decoder.dataLines = nil
	decoder.eventBytes = 0
	decoder.retry = nil
	return event, true, nil
}
