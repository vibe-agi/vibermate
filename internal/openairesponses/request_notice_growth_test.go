package openairesponses

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

// These serial tests catch growing-prefix report copies, dropped/reordered
// notices, truncated history, and inclusion of an unsuccessful item's report.
func TestResponsesRequestNoticeAllocationGrowth(t *testing.T) {
	codec := newTestCodec(t)
	for _, compatible := range []bool{false, true} {
		t.Run(fmt.Sprintf("compatible=%t", compatible), func(t *testing.T) {
			decode := codec.DecodeClientRequest
			if compatible {
				decode = codec.DecodeCompatibleClientRequest
			}
			measure := func(n int) uint64 {
				body := responsesNoticeFixture(t, n, compatible, -1)
				if _, _, err := decode(body); err != nil {
					t.Fatal(err)
				}
				var request protocolcore.Request
				var report protocolcore.TranslationReport
				var err error
				var elapsed time.Duration
				allocated := measuredBytes(t, func() { start := time.Now(); request, report, err = decode(body); elapsed = time.Since(start) })
				if err != nil {
					t.Fatal(err)
				}
				assertResponsesNoticePrefix(t, report, n-1, compatible)
				if len(request.Messages) != n {
					t.Fatalf("messages=%d want=%d", len(request.Messages), n)
				}
				for i, message := range request.Messages {
					wantText, wantRole := "synthetic", protocolcore.RoleAssistant
					if i == n-1 {
						wantText, wantRole = "tail sentinel", protocolcore.RoleUser
					}
					if message.Role != wantRole || len(message.Blocks) != 1 || message.Blocks[0].Text != wantText {
						t.Fatalf("message[%d]=%#v", i, message)
					}
				}
				t.Logf("n=%d allocated_bytes=%d elapsed=%s", n, allocated, elapsed)
				return allocated
			}
			small, large := measure(512), measure(2048)
			if large > 6*small+(8<<20) {
				t.Fatalf("request notice allocation grows faster than bounded linear work: small=%d large=%d", small, large)
			}
		})
	}
}

func TestResponsesRequestNoticeLateErrorPrefix(t *testing.T) {
	codec := newTestCodec(t)
	for _, compatible := range []bool{false, true} {
		decode := codec.DecodeClientRequest
		if compatible {
			decode = codec.DecodeCompatibleClientRequest
		}
		_, report, err := decode(responsesNoticeFixture(t, 2048, compatible, 2040))
		if protocolcore.ReasonOf(err) != protocolcore.ReasonInvalidClientRequest || !strings.Contains(err.Error(), "$.input[2040].role") {
			t.Fatalf("late error=%v", err)
		}
		assertResponsesNoticePrefix(t, report, 2040, compatible)
	}
}

func TestResponsesRequestNoticeIncludeAllocationGrowth(t *testing.T) {
	codec := newTestCodec(t)
	measure := func(n int) uint64 {
		include := make([]string, n)
		for i := range include {
			include[i] = fmt.Sprintf("native-fixture-%d", i)
		}
		body, err := json.Marshal(map[string]any{"model": "fixture", "input": "tail sentinel", "include": include})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := codec.DecodeCompatibleClientRequest(body); err != nil {
			t.Fatal(err)
		}
		var request protocolcore.Request
		var report protocolcore.TranslationReport
		allocated := measuredBytes(t, func() { request, report, err = codec.DecodeCompatibleClientRequest(body) })
		if err != nil {
			t.Fatal(err)
		}
		if len(request.Messages) != 1 || request.Messages[0].Blocks[0].Text != "tail sentinel" {
			t.Fatal("include decode lost input")
		}
		notices := report.Notices()
		if len(notices) != n {
			t.Fatalf("include notices=%d want=%d", len(notices), n)
		}
		for i, notice := range notices {
			want := protocolcore.TranslationNotice{Code: protocolcore.NoticeNativeContentNotProjected, Path: fmt.Sprintf("$.include[%d]", i)}
			if notice != want {
				t.Fatalf("include notice[%d]=%#v want=%#v", i, notice, want)
			}
		}
		t.Logf("n=%d allocated_bytes=%d", n, allocated)
		return allocated
	}
	small, large := measure(512), measure(2048)
	if large > 6*small+(8<<20) {
		t.Fatalf("request notice allocation grows faster than bounded linear work: small=%d large=%d", small, large)
	}
}

func TestResponsesRequestNoticeRemainingCountGuard(t *testing.T) {
	codec := newTestCodec(t)
	for _, compatible := range []bool{false, true} {
		decode := codec.DecodeClientRequest
		if compatible {
			decode = codec.DecodeCompatibleClientRequest
		}
		for _, n := range []int{4000, 4111} {
			body := responsesNoticeFixture(t, n, compatible, -1)
			var request protocolcore.Request
			var report protocolcore.TranslationReport
			var err error
			var elapsed time.Duration
			allocated := measuredBytes(t, func() { start := time.Now(); request, report, err = decode(body); elapsed = time.Since(start) })
			t.Logf("compatible=%t n=%d allocated_bytes=%d elapsed=%s error=%v", compatible, n, allocated, elapsed, err)
			assertResponsesNoticePrefix(t, report, n-1, compatible)
			if err != nil || len(request.Messages) != n || request.Messages[n-1].Blocks[0].Text != "tail sentinel" {
				t.Fatalf("default decode messages=%d error=%v", len(request.Messages), err)
			}
		}
	}
}

func responsesNoticeFixture(t *testing.T, n int, compatible bool, invalidIndex int) []byte {
	t.Helper()
	items := make([]any, n)
	for i := range items {
		role := "assistant"
		if i == invalidIndex {
			role = "invalid"
		}
		item := map[string]any{"type": "message", "id": fmt.Sprintf("item-%d", i), "role": role, "content": []any{map[string]string{"type": "output_text", "text": "synthetic"}}}
		if compatible {
			item["phase"] = "commentary"
		}
		items[i] = item
	}
	items[n-1] = map[string]any{"type": "message", "role": "user", "content": []any{map[string]string{"type": "input_text", "text": "tail sentinel"}}}
	body, err := json.Marshal(map[string]any{"model": "fixture", "input": items})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func assertResponsesNoticePrefix(t *testing.T, report protocolcore.TranslationReport, count int, compatible bool) {
	t.Helper()
	notices := report.Notices()
	perItem := 1
	if compatible {
		perItem = 2
	}
	if len(notices) != count*perItem {
		t.Fatalf("notices=%d want=%d", len(notices), count*perItem)
	}
	for i := 0; i < count; i++ {
		position := i * perItem
		if compatible {
			want := protocolcore.TranslationNotice{Code: protocolcore.NoticeMessagePhaseNotProjected, Path: fmt.Sprintf("$.input[%d].phase", i)}
			if notices[position] != want {
				t.Fatalf("notice[%d]=%#v want=%#v", position, notices[position], want)
			}
			position++
		}
		want := protocolcore.TranslationNotice{Code: protocolcore.NoticeMessageItemIdentityNotForwarded, Path: fmt.Sprintf("$.input[%d].id", i)}
		if notices[position] != want {
			t.Fatalf("notice[%d]=%#v want=%#v", position, notices[position], want)
		}
	}
}

func measuredBytes(t *testing.T, run func()) uint64 {
	t.Helper()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	run()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}
