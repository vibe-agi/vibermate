package protocolcore

import (
	"fmt"
	"slices"
	"testing"
)

// A caller mutation or later append must never alter an immutable snapshot.
func TestTranslationReportBuilderOwnsSnapshots(t *testing.T) {
	seed := []TranslationNotice{{Code: NoticeMessageItemIdentityNotForwarded, Path: "$.input[0].id"}}
	input := NewTranslationReport(seed...)
	var builder TranslationReportBuilder
	if !builder.Build().Empty() {
		t.Fatal("zero-value builder is not empty")
	}
	builder.Append(TranslationReport{})
	builder.Append(input)
	first := builder.Build()
	seed[0].Path = "mutated caller input"
	observed := first.Notices()
	observed[0].Path = "mutated returned slice"
	builder.Append(NewTranslationReport(TranslationNotice{Code: NoticeMessagePhaseNotProjected, Path: "$.input[0].phase"}))
	second := builder.Build()
	builder.Append(input) // Duplicates are observable and must be retained.
	third := builder.Build()
	wantFirst := []TranslationNotice{{Code: NoticeMessageItemIdentityNotForwarded, Path: "$.input[0].id"}}
	wantSecond := []TranslationNotice{
		{Code: NoticeMessageItemIdentityNotForwarded, Path: "$.input[0].id"},
		{Code: NoticeMessagePhaseNotProjected, Path: "$.input[0].phase"},
	}
	wantThird := []TranslationNotice{
		{Code: NoticeMessageItemIdentityNotForwarded, Path: "$.input[0].id"},
		{Code: NoticeMessagePhaseNotProjected, Path: "$.input[0].phase"},
		{Code: NoticeMessageItemIdentityNotForwarded, Path: "$.input[0].id"},
	}
	if !slices.Equal(first.Notices(), wantFirst) || !slices.Equal(input.Notices(), wantFirst) {
		t.Fatal("caller mutation or later append changed an immutable report")
	}
	if !slices.Equal(second.Notices(), wantSecond) || !slices.Equal(third.Notices(), wantThird) {
		t.Fatal("builder lost or reordered notices")
	}
	secondNotices := second.Notices()
	secondNotices[0].Code = NoticeMessagePhaseNotProjected
	if !slices.Equal(first.Notices(), wantFirst) || !slices.Equal(second.Notices(), wantSecond) || !slices.Equal(third.Notices(), wantThird) {
		t.Fatal("built snapshots share writable notice slices")
	}
}

func TestTranslationReportBuilderManyAppendsPreserveEveryEntry(t *testing.T) {
	var builder TranslationReportBuilder
	var snapshots []TranslationReport
	for i := 0; i < 2048; i++ {
		builder.Append(NewTranslationReport(TranslationNotice{Code: NoticeMessageItemIdentityNotForwarded, Path: fmt.Sprintf("$.input[%d].id", i)}))
		if i == 0 || i == 511 || i == 2047 {
			snapshots = append(snapshots, builder.Build())
		}
	}
	for index, count := range []int{1, 512, 2048} {
		notices := snapshots[index].Notices()
		if len(notices) != count {
			t.Fatalf("snapshot[%d] length=%d want=%d", index, len(notices), count)
		}
		for i, notice := range notices {
			want := TranslationNotice{Code: NoticeMessageItemIdentityNotForwarded, Path: fmt.Sprintf("$.input[%d].id", i)}
			if notice != want {
				t.Fatalf("snapshot[%d] entry[%d]=%#v want=%#v", index, i, notice, want)
			}
		}
	}
}
