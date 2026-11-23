package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
)

// maxKeyFileBytes bounds the key file read; an API key is far shorter.
const maxKeyFileBytes = 4 << 10

// apiKey holds the bearer secret. It has no String method that reveals the
// value, so a stray %v prints a placeholder rather than the key.
type apiKey struct{ value string }

func (key apiKey) String() string   { return "[redacted]" }
func (key apiKey) GoString() string { return "[redacted]" }
func (key apiKey) empty() bool      { return key.value == "" }

// loadKey reads the key from keyFile, whose mode must keep group and others
// out (0600 or stricter), or else from OPENAI_API_KEY. No error message
// carries any byte of the file or the variable.
func loadKey(keyFile string, getenv func(string) string) (apiKey, error) {
	if keyFile != "" {
		info, err := os.Stat(keyFile)
		if err != nil {
			return apiKey{}, fmt.Errorf("key file %s cannot be read: %w", keyFile, errors.Unwrap(err))
		}
		if !info.Mode().IsRegular() {
			return apiKey{}, fmt.Errorf("key file %s is not a regular file", keyFile)
		}
		if permission := info.Mode().Perm(); permission&0o077 != 0 {
			return apiKey{}, fmt.Errorf("key file %s has mode %04o; it must be 0600 or stricter (run: chmod 600 %s)", keyFile, permission, keyFile)
		}
		file, err := os.Open(keyFile)
		if err != nil {
			return apiKey{}, fmt.Errorf("key file %s cannot be opened", keyFile)
		}
		defer file.Close()
		content, err := io.ReadAll(io.LimitReader(file, maxKeyFileBytes+1))
		if err != nil {
			return apiKey{}, fmt.Errorf("key file %s cannot be read", keyFile)
		}
		if len(content) > maxKeyFileBytes {
			return apiKey{}, fmt.Errorf("key file %s is larger than an API key", keyFile)
		}
		return validKey(string(content), "key file "+keyFile)
	}
	if value := getenv("OPENAI_API_KEY"); value != "" {
		return validKey(value, "OPENAI_API_KEY")
	}
	return apiKey{}, errors.New("live mode needs an API key: pass --key-file <path> (mode 0600) or set OPENAI_API_KEY")
}

func validKey(raw, source string) (apiKey, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return apiKey{}, fmt.Errorf("%s is empty", source)
	}
	if len(value) > 1024 {
		return apiKey{}, fmt.Errorf("%s is longer than an API key", source)
	}
	for index := 0; index < len(value); index++ {
		if value[index] <= ' ' || value[index] > '~' {
			return apiKey{}, fmt.Errorf("%s holds a character an API key cannot contain (whitespace, control or non-ASCII)", source)
		}
	}
	return apiKey{value: value}, nil
}

// keyShapes are provider key spellings, masked or not, that an upstream
// message may echo (OpenAI's 401 shows a masked key). They are removed from
// every upstream-derived string the report keeps.
var keyShapes = regexp.MustCompile(`(?:sk|rk|pk)-[A-Za-z0-9_*.\-]{3,}`)

const redacted = "[redacted]"

// scrubber removes the key from everything the tool prints or writes.
type scrubber struct {
	secret []byte
	// tail is the key's last characters, which a masked echo keeps.
	tail []byte
}

func newScrubber(key apiKey) *scrubber {
	scrub := &scrubber{}
	if !key.empty() {
		scrub.secret = []byte(key.value)
		if len(key.value) >= 12 {
			scrub.tail = []byte(key.value[len(key.value)-8:])
		}
	}
	return scrub
}

// bytes replaces the key and its tail; it is safe on any output.
func (scrub *scrubber) bytes(value []byte) []byte {
	if len(scrub.secret) == 0 {
		return value
	}
	if bytes.Contains(value, scrub.secret) {
		value = bytes.ReplaceAll(value, scrub.secret, []byte(redacted))
	}
	if len(scrub.tail) != 0 && bytes.Contains(value, scrub.tail) {
		value = bytes.ReplaceAll(value, scrub.tail, []byte(redacted))
	}
	return value
}

func (scrub *scrubber) string(value string) string { return string(scrub.bytes([]byte(value))) }

// upstream cleans a string that came from the provider: the key, any key
// shape, control characters, and everything past limit runes.
func (scrub *scrubber) upstream(value string, limit int) string {
	value = scrub.string(value)
	value = keyShapes.ReplaceAllString(value, redacted)
	value = strings.Map(func(character rune) rune {
		if character < ' ' || character == 0x7f {
			return ' '
		}
		return character
	}, value)
	if runes := []rune(value); len(runes) > limit {
		value = string(runes[:limit]) + "..."
	}
	return value
}

// writer wraps an output so no write can carry the key.
func (scrub *scrubber) writer(output io.Writer) io.Writer {
	return &scrubWriter{scrub: scrub, output: output}
}

type scrubWriter struct {
	mutex  sync.Mutex
	scrub  *scrubber
	output io.Writer
}

func (writer *scrubWriter) Write(value []byte) (int, error) {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	if _, err := writer.output.Write(writer.scrub.bytes(value)); err != nil {
		return 0, err
	}
	return len(value), nil
}

// writeFile writes a file through the scrubber.
func (scrub *scrubber) writeFile(path string, content []byte) error {
	return os.WriteFile(path, scrub.bytes(content), 0o600)
}

// loggedHeaders are the response headers the transport log and the report
// keep; everything else (organization, project, cookies) is left out.
var loggedHeaders = []string{
	"Content-Type",
	"Openai-Processing-Ms",
	"Openai-Version",
	"Retry-After",
	"Retry-After-Ms",
	"X-Request-Id",
	"X-Ratelimit-Limit-Requests",
	"X-Ratelimit-Limit-Tokens",
	"X-Ratelimit-Remaining-Requests",
	"X-Ratelimit-Remaining-Tokens",
	"X-Ratelimit-Reset-Requests",
	"X-Ratelimit-Reset-Tokens",
}

func (scrub *scrubber) allowedHeaders(header http.Header) map[string]string {
	kept := map[string]string{}
	for _, name := range loggedHeaders {
		if value := header.Get(name); value != "" {
			kept[strings.ToLower(name)] = scrub.upstream(value, 100)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

// loggingTransport logs each request and response head. Authorization is
// never logged; request headers are named with their values only when they
// are known to be harmless.
type loggingTransport struct {
	next  http.RoundTripper
	log   io.Writer
	scrub *scrubber
}

func (transport *loggingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	var names []string
	for name := range request.Header {
		switch http.CanonicalHeaderKey(name) {
		case "Authorization":
			names = append(names, name+": "+redacted)
		case "Accept", "Content-Type", "User-Agent":
			names = append(names, name+": "+request.Header.Get(name))
		default:
			names = append(names, name+": (not logged)")
		}
	}
	fmt.Fprintf(transport.log, "-> %s %s (%d bytes) [%s]\n", request.Method, request.URL.Redacted(), request.ContentLength, strings.Join(slices.Sorted(slices.Values(names)), "; "))
	response, err := transport.next.RoundTrip(request)
	if err != nil {
		fmt.Fprintf(transport.log, "<- %s %s: %s\n", request.Method, request.URL.Redacted(), transport.scrub.upstream(err.Error(), 300))
		return nil, err
	}
	var kept []string
	for name, value := range transport.scrub.allowedHeaders(response.Header) {
		kept = append(kept, name+": "+value)
	}
	fmt.Fprintf(transport.log, "<- %d %s [%s]\n", response.StatusCode, request.URL.Redacted(), strings.Join(slices.Sorted(slices.Values(kept)), "; "))
	return response, nil
}
