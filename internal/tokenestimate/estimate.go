// Package tokenestimate estimates how many input tokens a model request
// costs, from the values a provider side plans to send. It is neutral and
// deterministic: it knows no wire dialect and no tokenizer, only the
// counting rules below, so equal inputs always give equal estimates and
// adding content never lowers one. It answers count_tokens locally on a
// translated route, where no upstream endpoint can (design 10).
//
// The rules:
//   - text costs ceil(ASCII bytes / 4) plus one token per non-ASCII rune;
//   - every input item costs PerItem more, every tool PerTool more, and a
//     JSON schema costs ceil(bytes / 4);
//   - an image costs the provider's high-detail tile price, read from its
//     header only, or LowDetailImageTokens at low detail;
//   - a PDF costs PDFPageTokens per page, counting its page objects.
package tokenestimate

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"strings"
	"unicode/utf8"

	"golang.org/x/image/webp"
)

const (
	// PerItem is the framing cost of one input item: a message, a tool call
	// or a tool output.
	PerItem = 4
	// PerTool is the framing cost of one declared tool.
	PerTool = 8
	// PDFPageTokens is the cost of one PDF page.
	PDFPageTokens = 1500
	// LowDetailImageTokens is the fixed cost of a low-detail image.
	LowDetailImageTokens = 85
	// DefaultImageTokens is the cost of an image whose size cannot be read
	// (a URL, or a header that does not decode): the high-detail price of a
	// 1024x1024 image.
	DefaultImageTokens = 765

	imageBaseTokens = 85
	imageTileTokens = 170
	imageTileSide   = 512
	imageMaxSide    = 2048
	imageShortSide  = 768
)

// Text estimates a string: ceil(ASCII bytes / 4) plus one token per
// non-ASCII rune. An invalid UTF-8 byte counts as one rune.
func Text(value string) int {
	ascii, runes := 0, 0
	for index := 0; index < len(value); {
		if value[index] < utf8.RuneSelf {
			ascii++
			index++
			continue
		}
		_, size := utf8.DecodeRuneInString(value[index:])
		runes++
		index += size
	}
	return (ascii+3)/4 + runes
}

// Schema estimates a JSON schema of the given byte length.
func Schema(byteLength int) int {
	if byteLength <= 0 {
		return 0
	}
	return (byteLength + 3) / 4
}

// ImageTokens is the high-detail tile price of an image of the given pixel
// size: fit within 2048x2048, scale the shorter side down to 768, then 85
// plus 170 per 512-pixel tile. A size that is not positive costs
// DefaultImageTokens.
func ImageTokens(width, height int) int {
	if width <= 0 || height <= 0 {
		return DefaultImageTokens
	}
	w, h := float64(width), float64(height)
	if longest := math.Max(w, h); longest > imageMaxSide {
		w, h = scaleSides(w, h, imageMaxSide/longest)
	}
	if shortest := math.Min(w, h); shortest > imageShortSide {
		w, h = scaleSides(w, h, imageShortSide/shortest)
	}
	tiles := tilesAlong(w) * tilesAlong(h)
	return imageBaseTokens + imageTileTokens*tiles
}

// scaleSides scales both sides and rounds them to whole pixels, so a side
// that lands on a tile boundary does not spill into another tile through
// floating-point error.
func scaleSides(w, h, factor float64) (float64, float64) {
	return math.Max(1, math.Round(w*factor)), math.Max(1, math.Round(h*factor))
}

func tilesAlong(side float64) int {
	return (int(side) + imageTileSide - 1) / imageTileSide
}

// InlineImage estimates a base64 image of the given media type. Only the
// image header is decoded, through a streaming base64 reader, so the cost
// does not depend on the payload size. A payload whose header does not
// decode as any supported format costs DefaultImageTokens.
func InlineImage(mediaType, data string) int {
	width, height, ok := ImageSize(mediaType, data)
	if !ok {
		return DefaultImageTokens
	}
	return ImageTokens(width, height)
}

type configDecoder func(io.Reader) (image.Config, error)

var imageConfigDecoders = []struct {
	mediaType string
	decode    configDecoder
}{
	{"image/png", png.DecodeConfig},
	{"image/jpeg", jpeg.DecodeConfig},
	{"image/gif", gif.DecodeConfig},
	{"image/webp", webp.DecodeConfig},
}

// ImageSize reads the pixel size from a base64 image's header. The declared
// media type is tried first, then the other supported formats, since a
// client may mislabel an image. Formats are decoded through their own
// packages, so nothing is registered with package image.
func ImageSize(mediaType, data string) (width, height int, ok bool) {
	return imageSize(mediaType, func() io.Reader {
		return base64.NewDecoder(base64.StdEncoding, strings.NewReader(data))
	})
}

// imageSize tries each config decoder on a fresh reader from open.
func imageSize(mediaType string, open func() io.Reader) (width, height int, ok bool) {
	attempt := func(decode configDecoder) bool {
		config, err := decode(open())
		if err != nil || config.Width <= 0 || config.Height <= 0 {
			return false
		}
		width, height = config.Width, config.Height
		return true
	}
	for _, candidate := range imageConfigDecoders {
		if candidate.mediaType == mediaType && attempt(candidate.decode) {
			return width, height, true
		}
	}
	for _, candidate := range imageConfigDecoders {
		if candidate.mediaType != mediaType && attempt(candidate.decode) {
			return width, height, true
		}
	}
	return 0, 0, false
}

// InlinePDF estimates a base64 PDF at PDFPageTokens per page.
func InlinePDF(data string) int {
	return PDFPages(base64.NewDecoder(base64.StdEncoding, strings.NewReader(data))) * PDFPageTokens
}

// pdfScanChunk is how much of a PDF is read at a time; pdfCarry is enough
// of the previous chunk to finish a page marker that straddles two.
const (
	pdfScanChunk     = 64 << 10
	pdfMaxWhitespace = 16
	pdfCarry         = len("/Type") + pdfMaxWhitespace + len("/Page") + 1
)

var pdfTypeKey = []byte("/Type")

// PDFPages counts the page objects of a PDF, each "/Type /Page" whose name
// is not "/Pages", in one pass over a bounded window. A document whose page
// objects are all compressed out of sight still counts as one page; so does
// one that cannot be read.
func PDFPages(reader io.Reader) int {
	pages := 0
	buffer := make([]byte, 0, pdfScanChunk+pdfCarry)
	chunk := make([]byte, pdfScanChunk)
	for {
		count, err := io.ReadFull(reader, chunk)
		buffer = append(buffer, chunk[:count]...)
		final := err != nil
		consumed := 0
		for {
			found := bytes.Index(buffer[consumed:], pdfTypeKey)
			if found < 0 {
				break
			}
			start := consumed + found
			page, decided := pdfPageAt(buffer[start:], final)
			if !decided {
				break
			}
			if page {
				pages++
			}
			consumed = start + len(pdfTypeKey)
		}
		if final {
			break
		}
		// Keep only what a straddling marker could still need.
		keep := max(consumed, len(buffer)-pdfCarry)
		buffer = append(buffer[:0], buffer[keep:]...)
	}
	return max(1, pages)
}

// pdfPageAt decides whether the "/Type" at the start of window names a page
// object. decided is false when the window ends before the answer is known
// and more input may follow.
func pdfPageAt(window []byte, final bool) (page, decided bool) {
	position := len(pdfTypeKey)
	for spaces := 0; position < len(window) && pdfWhitespace(window[position]); spaces++ {
		if spaces == pdfMaxWhitespace {
			return false, true
		}
		position++
	}
	const name = "/Page"
	if len(window)-position < len(name)+1 {
		if !final {
			return false, false
		}
		// At the end of the document "/Page" may be the last name.
		return string(window[position:]) == name, true
	}
	if string(window[position:position+len(name)]) != name {
		return false, true
	}
	next := window[position+len(name)]
	regular := (next >= 'a' && next <= 'z') || (next >= 'A' && next <= 'Z') || (next >= '0' && next <= '9')
	return !regular, true
}

func pdfWhitespace(value byte) bool {
	switch value {
	case ' ', '\t', '\r', '\n', '\f', 0:
		return true
	default:
		return false
	}
}
