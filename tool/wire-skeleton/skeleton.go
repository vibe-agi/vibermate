package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"regexp"
	"strings"
)

// DefaultClip is the string length above which prose is replaced, matching
// the clip length of the analysis script this tool ports.
const DefaultClip = 160

// Options controls one skeleton pass. The zero value is not useful; start
// from DefaultOptions.
type Options struct {
	// Clip is the decoded byte length above which a prose string is replaced
	// by a placeholder of exactly the same length.
	Clip int
	// Redact lists extra literal values (for example a user name) that are
	// replaced by an equally long run of 'x' wherever they occur.
	Redact []string
}

func DefaultOptions() Options {
	return Options{Clip: DefaultClip}
}

// proseKeys are the object members whose string values are free prose. Only
// these are ever replaced by placeholders; every other string keeps its
// value, apart from identifier rewriting, because ViberMate may interpret it.
var proseKeys = map[string]bool{
	"text":         true,
	"description":  true,
	"content":      true,
	"instructions": true,
	"thinking":     true,
	"output":       true,
}

// billingPrefix marks Claude Code's attribution line. ViberMate interprets it
// by prefix, so it is never replaced as prose.
const billingPrefix = "x-anthropic-billing-header:"

var (
	uuidPattern   = regexp.MustCompile(`[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}`)
	hexRunPattern = regexp.MustCompile(`[0-9a-f]{32,}`)
	cchPattern    = regexp.MustCompile(`cch=[0-9A-Fa-f]+`)
	tmpPattern    = regexp.MustCompile(`/tmp/[^/\s"\\]+`)
	homePattern   = regexp.MustCompile(`/(home|Users)/[^/\s"\\]+`)
	// Claude Code names per-project directories after the project path with
	// every slash turned into a dash.
	tmpSlugPattern  = regexp.MustCompile(`-tmp-[^/\s"\\]+`)
	homeSlugPattern = regexp.MustCompile(`-(home|Users)-[^-/\s"\\]+`)
	dataURLPrefix   = regexp.MustCompile(`^data:([a-z]+/[a-z0-9.+-]+);base64,`)
)

// Skeleton rewrites one JSON document. Object member order, duplicate
// members, number spellings and every structural value are kept; prose is
// replaced by same-length placeholders, identifiers by deterministic
// look-alikes, and base64 media by tiny valid files of the same media type.
func Skeleton(document []byte, options Options) ([]byte, error) {
	if options.Clip <= 0 {
		return nil, errors.New("clip length must be positive")
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	rewriter := &rewriter{decoder: decoder, options: options}
	if err := rewriter.value(""); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("document has trailing data")
	}
	return rewriter.output.Bytes(), nil
}

// SkeletonEventStream rewrites every data line of a server-sent event stream
// with the same rules and keeps every other line, and the framing, unchanged.
func SkeletonEventStream(stream []byte, options Options) ([]byte, error) {
	var output bytes.Buffer
	scanner := bufio.NewScanner(bytes.NewReader(stream))
	scanner.Buffer(make([]byte, 0, 64<<10), 64<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if payload, ok := strings.CutPrefix(line, "data: "); ok && payload != "[DONE]" {
			rewritten, err := Skeleton([]byte(payload), options)
			if err != nil {
				return nil, fmt.Errorf("event data: %w", err)
			}
			line = "data: " + string(rewritten)
		}
		output.WriteString(line)
		output.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

type rewriter struct {
	decoder *json.Decoder
	options Options
	output  bytes.Buffer
}

// value copies one JSON value. key is the name of the member that holds it,
// or "" for array elements and the root.
func (r *rewriter) value(key string) error {
	token, err := r.decoder.Token()
	if err != nil {
		return err
	}
	switch typed := token.(type) {
	case json.Delim:
		switch typed {
		case '{':
			return r.object()
		case '[':
			return r.array()
		default:
			return errors.New("unexpected closing delimiter")
		}
	case string:
		r.writeString(r.rewriteString(key, typed))
	case json.Number:
		r.output.WriteString(typed.String())
	case bool:
		if typed {
			r.output.WriteString("true")
		} else {
			r.output.WriteString("false")
		}
	case nil:
		r.output.WriteString("null")
	default:
		return fmt.Errorf("unexpected token %T", token)
	}
	return nil
}

func (r *rewriter) object() error {
	r.output.WriteByte('{')
	media := mediaObject{}
	first := true
	// A base64 source's data member is replaced once the whole object has
	// been read, because its media_type can follow it.
	for r.decoder.More() {
		token, err := r.decoder.Token()
		if err != nil {
			return err
		}
		name, ok := token.(string)
		if !ok {
			return errors.New("object member name is not a string")
		}
		if !first {
			r.output.WriteByte(',')
		}
		first = false
		r.writeString(name)
		r.output.WriteByte(':')
		start := r.output.Len()
		if err := r.value(name); err != nil {
			return err
		}
		media.observe(name, r.output.Bytes()[start:], start)
	}
	if _, err := r.decoder.Token(); err != nil {
		return err
	}
	if media.isBase64Source() {
		replacement, err := tinyMedia(media.mediaType)
		if err == nil {
			r.replaceRange(media.dataStart, media.dataEnd, replacement)
		}
	}
	r.output.WriteByte('}')
	return nil
}

func (r *rewriter) array() error {
	r.output.WriteByte('[')
	first := true
	for r.decoder.More() {
		if !first {
			r.output.WriteByte(',')
		}
		first = false
		if err := r.value(""); err != nil {
			return err
		}
	}
	if _, err := r.decoder.Token(); err != nil {
		return err
	}
	r.output.WriteByte(']')
	return nil
}

// replaceRange swaps one already written string value for another.
func (r *rewriter) replaceRange(start, end int, value string) {
	tail := bytes.Clone(r.output.Bytes()[end:])
	r.output.Truncate(start)
	r.writeString(value)
	r.output.Write(tail)
}

func (r *rewriter) rewriteString(key, value string) string {
	if prefix := dataURLPrefix.FindStringSubmatch(value); prefix != nil {
		if replacement, err := tinyMedia(prefix[1]); err == nil {
			return prefix[0] + replacement
		}
	}
	value = r.rewriteIdentifiers(value)
	if proseKeys[key] && len(value) > r.options.Clip && !strings.HasPrefix(value, billingPrefix) {
		return Placeholder(value)
	}
	return value
}

func (r *rewriter) rewriteIdentifiers(value string) string {
	for _, literal := range r.options.Redact {
		if literal != "" {
			value = strings.ReplaceAll(value, literal, strings.Repeat("x", len(literal)))
		}
	}
	value = uuidPattern.ReplaceAllStringFunc(value, fakeUUID)
	value = hexRunPattern.ReplaceAllStringFunc(value, func(match string) string {
		return fakeHex("hex", match, len(match))
	})
	value = cchPattern.ReplaceAllStringFunc(value, func(match string) string {
		digits := strings.TrimPrefix(match, "cch=")
		return "cch=" + fakeHex("cch", digits, len(digits))
	})
	value = tmpPattern.ReplaceAllString(value, "/tmp/wire-skeleton")
	value = homePattern.ReplaceAllString(value, "/$1/user")
	value = tmpSlugPattern.ReplaceAllString(value, "-tmp-wire-skeleton")
	value = homeSlugPattern.ReplaceAllString(value, "-$1-user")
	return value
}

func (r *rewriter) writeString(value string) {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	// Encoding a Go string cannot fail.
	_ = encoder.Encode(value)
	r.output.Write(bytes.TrimSuffix(encoded.Bytes(), []byte("\n")))
}

// mediaObject recognises an Anthropic base64 source object:
// {"type":"base64","media_type":...,"data":...} in any member order.
type mediaObject struct {
	base64             bool
	mediaType          string
	dataStart, dataEnd int
	hasData            bool
}

func (media *mediaObject) observe(name string, encoded []byte, start int) {
	switch name {
	case "type":
		var value string
		media.base64 = json.Unmarshal(encoded, &value) == nil && value == "base64"
	case "media_type":
		_ = json.Unmarshal(encoded, &media.mediaType)
	case "data":
		var value string
		if json.Unmarshal(encoded, &value) == nil {
			media.hasData = true
			media.dataStart, media.dataEnd = start, start+len(encoded)
		}
	}
}

func (media mediaObject) isBase64Source() bool {
	return media.base64 && media.hasData && media.mediaType != ""
}

// Placeholder returns deterministic ASCII text with exactly len(original)
// bytes. Equal inputs give equal placeholders, so repeated prose (a system
// prompt sent on every request) stays repeated.
func Placeholder(original string) string {
	digest := sha256.Sum256([]byte("wire-skeleton/prose\x00" + original))
	marker := "[prose " + hex.EncodeToString(digest[:4]) + "] "
	filler := "lorem ipsum dolor sit amet "
	var builder strings.Builder
	builder.Grow(len(original))
	if len(marker) <= len(original) {
		builder.WriteString(marker)
	}
	for builder.Len() < len(original) {
		remaining := len(original) - builder.Len()
		if remaining >= len(filler) {
			builder.WriteString(filler)
		} else {
			builder.WriteString(filler[:remaining])
		}
	}
	return builder.String()
}

func fakeUUID(original string) string {
	digest := fakeHex("uuid", strings.ToLower(original), 32)
	// Keep the version digit so time-ordered and random identifiers keep
	// their shape.
	version := strings.ToLower(original[14:15])
	return digest[0:8] + "-" + digest[8:12] + "-" + version + digest[13:16] + "-" +
		digest[16:20] + "-" + digest[20:32]
}

func fakeHex(domain, original string, length int) string {
	var builder strings.Builder
	for counter := 0; builder.Len() < length; counter++ {
		digest := sha256.Sum256([]byte(fmt.Sprintf("wire-skeleton/%s\x00%d\x00%s", domain, counter, original)))
		builder.WriteString(hex.EncodeToString(digest[:]))
	}
	return builder.String()[:length]
}

// tinyMedia returns the base64 text of a minimal valid file of mediaType.
func tinyMedia(mediaType string) (string, error) {
	pixel := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	pixel.Set(0, 0, color.NRGBA{R: 0x2a, G: 0x6f, B: 0xdb, A: 0xff})
	var encoded bytes.Buffer
	switch mediaType {
	case "image/png":
		if err := png.Encode(&encoded, pixel); err != nil {
			return "", err
		}
	case "image/jpeg":
		if err := jpeg.Encode(&encoded, pixel, &jpeg.Options{Quality: 75}); err != nil {
			return "", err
		}
	case "image/gif":
		if err := gif.Encode(&encoded, pixel, nil); err != nil {
			return "", err
		}
	case "image/webp":
		// A 1x1 lossless WebP; the standard library has no WebP encoder.
		return "UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA==", nil
	case "application/pdf":
		encoded.WriteString(minimalPDF())
	default:
		return "", fmt.Errorf("media type %q has no tiny replacement", mediaType)
	}
	return base64.StdEncoding.EncodeToString(encoded.Bytes()), nil
}

// minimalPDF builds a one-page PDF with a correct cross-reference table.
func minimalPDF() string {
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 72 72] >>",
	}
	var document strings.Builder
	document.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects))
	for index, object := range objects {
		offsets[index] = document.Len()
		fmt.Fprintf(&document, "%d 0 obj\n%s\nendobj\n", index+1, object)
	}
	xref := document.Len()
	fmt.Fprintf(&document, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets {
		fmt.Fprintf(&document, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&document, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return document.String()
}
