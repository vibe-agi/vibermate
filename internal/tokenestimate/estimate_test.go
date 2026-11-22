package tokenestimate

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"strings"
	"testing"
	"time"
)

func TestTextCountsASCIIByQuartersAndOtherRunesOneEach(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		value string
		want  int
	}{
		{"empty", "", 0},
		{"one byte", "a", 1},
		{"four bytes", "abcd", 1},
		{"five bytes", "abcde", 2},
		{"prose", "Reply with the fixture text", 7},
		// Five CJK runes of three bytes each count one token each.
		{"cjk", "\u4f60\u597d\u4e16\u754c\u3002", 5},
		// Two ASCII bytes round up to one; two runes count two.
		{"mixed", "ok\u4f60\u597d", 3},
		{"emoji", "\U0001F600", 1},
		{"invalid byte", "a\xff", 2},
	} {
		if got := Text(test.value); got != test.want {
			t.Errorf("%s: Text(%q) = %d, want %d", test.name, test.value, got, test.want)
		}
	}
}

func TestTextIsDeterministicAndMonotonic(t *testing.T) {
	t.Parallel()

	pieces := []string{"You are a Claude agent. ", "\u4f60\u597d", "<system-reminder>", " ", "\u00e9", "x"}
	previous := 0
	var builder strings.Builder
	for round := 0; round < 200; round++ {
		builder.WriteString(pieces[round%len(pieces)])
		value := builder.String()
		got := Text(value)
		if got < previous {
			t.Fatalf("Text decreased from %d to %d after appending %q", previous, got, pieces[round%len(pieces)])
		}
		if again := Text(value); again != got {
			t.Fatalf("Text is not deterministic: %d then %d", got, again)
		}
		previous = got
	}
}

func TestSchemaRoundsBytesUpToQuarters(t *testing.T) {
	t.Parallel()

	for length, want := range map[int]int{-1: 0, 0: 0, 1: 1, 4: 1, 5: 2, 400: 100} {
		if got := Schema(length); got != want {
			t.Errorf("Schema(%d) = %d, want %d", length, got, want)
		}
	}
}

func TestImageTokensFollowTheHighDetailTilePrice(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		width, height int
		want          int
	}{
		// OpenAI's published examples: 1024x1024 is 765, 2048x4096 is
		// 1105, and a small image is one tile.
		{1024, 1024, 765},
		{2048, 4096, 1105},
		{100, 100, 255},
		{512, 512, 255},
		{513, 512, 425},
		// The shorter side is only scaled down: 768x1536 stays as it is.
		{768, 1536, 1105},
		// Fitting within 2048 first: 4096x1024 becomes 2048x512.
		{4096, 1024, 85 + 170*4},
		{0, 10, DefaultImageTokens},
		{10, -1, DefaultImageTokens},
	} {
		if got := ImageTokens(test.width, test.height); got != test.want {
			t.Errorf("ImageTokens(%d, %d) = %d, want %d", test.width, test.height, got, test.want)
		}
	}
}

func encodedImage(t *testing.T, format string, width, height int) string {
	t.Helper()
	picture := image.NewPaletted(image.Rect(0, 0, width, height), color.Palette{color.Black, color.White})
	var encoded bytes.Buffer
	var err error
	switch format {
	case "png":
		err = png.Encode(&encoded, picture)
	case "jpeg":
		err = jpeg.Encode(&encoded, picture, nil)
	case "gif":
		err = gif.Encode(&encoded, picture, nil)
	case "webp":
		// No WebP encoder exists in the standard library or x/image, so
		// this is a lossless (VP8L) header written by hand: the RIFF
		// envelope, the chunk, its signature byte and the packed size.
		header := make([]byte, 0, 25)
		header = append(header, "RIFF"...)
		header = binary.LittleEndian.AppendUint32(header, 4+8+5)
		header = append(header, "WEBPVP8L"...)
		header = binary.LittleEndian.AppendUint32(header, 5)
		header = append(header, 0x2f)
		header = binary.LittleEndian.AppendUint32(header, uint32(width-1)|uint32(height-1)<<14)
		encoded.Write(header)
	}
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(encoded.Bytes())
}

func TestInlineImageReadsTheSizeFromTheHeader(t *testing.T) {
	t.Parallel()

	for _, format := range []string{"png", "jpeg", "gif", "webp"} {
		data := encodedImage(t, format, 1100, 600)
		width, height, ok := ImageSize("image/"+format, data)
		if !ok || width != 1100 || height != 600 {
			t.Fatalf("%s: ImageSize = %d x %d, %v; want 1100 x 600", format, width, height, ok)
		}
		// 1100x600 keeps its size (the shorter side is under 768): 3 x 2
		// tiles.
		if got := InlineImage("image/"+format, data); got != 85+170*6 {
			t.Fatalf("%s: InlineImage = %d, want %d", format, got, 85+170*6)
		}
	}
	// A mislabelled image is still read.
	if got := InlineImage("image/jpeg", encodedImage(t, "png", 100, 100)); got != 255 {
		t.Fatalf("mislabelled PNG = %d, want 255", got)
	}
	for _, data := range []string{"", "not base64!", base64.StdEncoding.EncodeToString([]byte("plain text"))} {
		if got := InlineImage("image/png", data); got != DefaultImageTokens {
			t.Fatalf("InlineImage(%q) = %d, want the default %d", data, got, DefaultImageTokens)
		}
	}
}

// countingReader counts the bytes a decoder reads.
type countingReader struct {
	reader io.Reader
	read   int
}

func (counter *countingReader) Read(buffer []byte) (int, error) {
	count, err := counter.reader.Read(buffer)
	counter.read += count
	return count, err
}

func TestInlineImageReadsOnlyTheHeader(t *testing.T) {
	t.Parallel()

	for _, format := range []string{"png", "jpeg", "gif", "webp"} {
		data := encodedImage(t, format, 300, 200)
		// The header followed by 16 MiB the decoders must never reach.
		raw, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			t.Fatal(err)
		}
		raw = append(raw, bytes.Repeat([]byte{0xa5}, 16<<20)...)
		var readers []*countingReader
		width, height, ok := imageSize("image/"+format, func() io.Reader {
			reader := &countingReader{reader: bytes.NewReader(raw)}
			readers = append(readers, reader)
			return reader
		})
		if !ok || width != 300 || height != 200 {
			t.Fatalf("%s: size = %d x %d, %v", format, width, height, ok)
		}
		for _, reader := range readers {
			if reader.read > 64<<10 {
				t.Fatalf("%s: a decoder read %d bytes for a header", format, reader.read)
			}
		}
	}
}

func pdfWithPages(pages int, separator string) string {
	var document strings.Builder
	document.WriteString("%PDF-1.7\n1 0 obj << /Type /Catalog /Pages 2 0 R >> endobj\n")
	document.WriteString("2 0 obj << /Type /Pages /Kids [] /Count 0 >> endobj\n")
	for page := 0; page < pages; page++ {
		document.WriteString("3 0 obj << /Type" + separator + "/Page /Parent 2 0 R >> endobj\n")
	}
	document.WriteString("%%EOF\n")
	return document.String()
}

func TestPDFPagesCountsPageObjectsOnly(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		document string
		want     int
	}{
		{"three pages", pdfWithPages(3, " "), 3},
		{"no space", pdfWithPages(2, ""), 2},
		{"line break", pdfWithPages(4, "\r\n"), 4},
		{"tree only", pdfWithPages(0, " "), 1},
		{"page at the end", "<< /Type /Page", 1},
		{"page at the end twice", "/Type /Page /Type /Page", 2},
		// Up to 16 whitespace bytes may separate the key from its name.
		{"sixteen whitespace bytes", pdfWithPages(1, " ") + "<< /Type" + strings.Repeat(" ", 16) + "/Page >>", 2},
		{"seventeen whitespace bytes", pdfWithPages(1, " ") + "<< /Type" + strings.Repeat(" ", 17) + "/Page >>", 1},
		{"page label is not a page", "<< /Type /PageLabel >> << /Type /Page>>", 1},
		{"empty", "", 1},
	} {
		if got := PDFPages(strings.NewReader(test.document)); got != test.want {
			t.Errorf("%s: PDFPages = %d, want %d", test.name, got, test.want)
		}
	}
	data := base64.StdEncoding.EncodeToString([]byte(pdfWithPages(3, " ")))
	if got := InlinePDF(data); got != 3*PDFPageTokens {
		t.Fatalf("InlinePDF = %d, want %d", got, 3*PDFPageTokens)
	}
	if got := InlinePDF("%%% not base64"); got != PDFPageTokens {
		t.Fatalf("unreadable PDF = %d, want one page", got)
	}
}

func TestPDFPagesFindsMarkersAcrossChunkBoundaries(t *testing.T) {
	t.Parallel()

	marker := "/Type /Page>>"
	for offset := pdfScanChunk - len(marker) - 2; offset <= pdfScanChunk+2; offset++ {
		// One whole page before the boundary, so a lost straddling marker
		// shows as one page instead of hiding behind the one-page floor.
		document := marker + strings.Repeat("x", offset-len(marker)) + marker + strings.Repeat("y", 10)
		if got := PDFPages(strings.NewReader(document)); got != 2 {
			t.Fatalf("marker at %d: PDFPages = %d, want 2", offset, got)
		}
		document = strings.Repeat("x", offset) + "/Type" + strings.Repeat(" ", 3) + "/Pages" + strings.Repeat("y", 10) + marker
		if got := PDFPages(strings.NewReader(document)); got != 1 {
			t.Fatalf("/Pages at %d counted as a page: PDFPages = %d", offset, got)
		}
	}
}

func TestPDFPagesReadsSixteenMebibytesInBoundedTime(t *testing.T) {
	t.Parallel()

	page := "4 0 obj << /Type /Page /Contents 5 0 R >> endobj\nstream " + strings.Repeat("q", 4000) + " endstream\n"
	var document strings.Builder
	pages := 0
	for document.Len() < 16<<20 {
		document.WriteString(page)
		pages++
	}
	data := base64.StdEncoding.EncodeToString([]byte(document.String()))
	started := time.Now()
	if got := InlinePDF(data); got != pages*PDFPageTokens {
		t.Fatalf("InlinePDF = %d, want %d", got, pages*PDFPageTokens)
	}
	// A generous bound: the scan is linear, and a quadratic one would take
	// far longer even on a loaded machine.
	if elapsed := time.Since(started); elapsed > time.Minute {
		t.Fatalf("a 16 MiB PDF took %v", elapsed)
	}
}

func TestTextOfSixteenMebibytesIsBounded(t *testing.T) {
	t.Parallel()

	value := strings.Repeat("abc\u4f60", 16<<20/6)
	started := time.Now()
	if got, want := Text(value), (len(value)/6*3+3)/4+len(value)/6; got != want {
		t.Fatalf("Text = %d, want %d", got, want)
	}
	if elapsed := time.Since(started); elapsed > time.Minute {
		t.Fatalf("16 MiB of text took %v", elapsed)
	}
}
