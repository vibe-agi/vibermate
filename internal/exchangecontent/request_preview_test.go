package exchangecontent

import (
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func TestPreviewRequestMessageUsesOnlyRecordedVisibleContent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		blocks []Block
		want   RequestPreview
		wantOK bool
	}{
		{
			name: "text is made safe for one compact line",
			blocks: []Block{{
				Kind: string(protocolcore.BlockText), Availability: AvailabilityRecorded,
				Text: "  inspect\n\tthe\u0000 request  ",
			}},
			want:   RequestPreview{Kind: string(protocolcore.BlockText), Text: "inspect the request"},
			wantOK: true,
		},
		{
			name: "tool call retains its qualified name",
			blocks: []Block{{
				Kind: string(protocolcore.BlockToolCall), Availability: AvailabilityRecorded,
				ToolNamespace: "filesystem", ToolName: "read",
			}},
			want:   RequestPreview{Kind: string(protocolcore.BlockToolCall), Text: "filesystem.read"},
			wantOK: true,
		},
		{
			name: "reasoning is never a list preview",
			blocks: []Block{{
				Kind: BlockKindReasoning, Availability: AvailabilityRecorded,
				Text: "private reasoning", ProviderSource: "provider", ProviderKind: "thinking",
			}},
			wantOK: false,
		},
		{
			name:   "omitted content is not invented",
			blocks: []Block{{Kind: string(protocolcore.BlockText), Availability: AvailabilityOmitted}},
			wantOK: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, ok := PreviewRequestMessage(Message{Role: "user", Blocks: test.blocks})
			if ok != test.wantOK || got != test.want {
				t.Fatalf("PreviewRequestMessage() = %+v, %t; want %+v, %t", got, ok, test.want, test.wantOK)
			}
		})
	}
}

func TestPreviewRequestMessageTruncatesByRune(t *testing.T) {
	t.Parallel()
	preview, ok := PreviewRequestMessage(Message{Role: "user", Blocks: []Block{{
		Kind: string(protocolcore.BlockText), Availability: AvailabilityRecorded,
		Text: strings.Repeat("\u754c", MaxRequestPreviewRunes+1),
	}}})
	if !ok || !preview.Truncated || len([]rune(preview.Text)) != MaxRequestPreviewRunes {
		t.Fatalf("preview = %+v, %t", preview, ok)
	}
}
