package desktopcontrol

import (
	"context"
	"testing"

	"github.com/vibe-agi/vibermate/internal/exchangecontent"
)

type previewReader struct{ exchangecontent.Reader }

func (previewReader) RequestPreviews(context.Context, []string) (map[string]exchangecontent.RequestPreview, error) {
	return map[string]exchangecontent.RequestPreview{
		"valid":   {Kind: "text", Text: "hello"},
		"invalid": {Kind: "text", Text: "trailing ", Truncated: true},
	}, nil
}

func (previewReader) AvailableBodies(context.Context, []string) (map[string]bool, error) {
	return map[string]bool{"valid": true, "invalid": true}, nil
}

func TestInvalidPreviewCannotPoisonAnActivityPage(t *testing.T) {
	page := ActivityPage{Items: []ActivitySummary{{ID: "invalid"}, {ID: "valid"}}}
	handler := Handler{contents: previewReader{}}
	if err := handler.attachActivityRequestPreviews(context.Background(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Items[0].RequestPreview != nil || page.Items[1].RequestPreview == nil || page.Items[1].RequestPreview.Text != "hello" {
		t.Fatalf("previews: %+v", page)
	}
	if page.Items[0].ContentAvailable == nil || !*page.Items[0].ContentAvailable {
		t.Fatal("bad preview hid retained content")
	}
}
