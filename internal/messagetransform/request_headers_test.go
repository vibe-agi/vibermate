package messagetransform

import (
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestRequestUserAgentOverride(t *testing.T) {
	t.Parallel()
	value := func(text string) *string { return &text }
	for _, test := range []struct {
		name          string
		before, after http.Header
		want          *string
		invalid       bool
	}{
		{name: "absent"},
		{name: "unchanged casing", before: http.Header{"user-agent": {"original"}}, after: http.Header{"UsEr-AgEnT": {"original"}}},
		{name: "unchanged Unicode is not an override", before: http.Header{"User-Agent": {"客户端"}}, after: http.Header{"User-Agent": {"客户端"}}},
		{name: "unchanged multiple is not an override", before: http.Header{"User-Agent": {"a", "b"}}, after: http.Header{"User-Agent": {"a", "b"}}},
		{name: "added", after: http.Header{"User-Agent": {"test/1.0"}}, want: value("test/1.0")},
		{name: "replaced", before: http.Header{"User-Agent": {"original"}}, after: http.Header{"User-Agent": {"edited"}}, want: value("edited")},
		{name: "deleted", before: http.Header{"User-Agent": {"original"}}, want: value("")},
		{name: "empty", after: http.Header{"User-Agent": {""}}, want: value("")},
		{name: "empty array", before: http.Header{"User-Agent": {"original"}}, after: http.Header{"User-Agent": {}}, want: value("")},
		{name: "maximum", after: http.Header{"User-Agent": {strings.Repeat("x", 512)}}, want: value(strings.Repeat("x", 512))},
		{name: "oversize", after: http.Header{"User-Agent": {strings.Repeat("x", 513)}}, invalid: true},
		{name: "multiple", after: http.Header{"User-Agent": {"a", "b"}}, invalid: true},
		{name: "case collision", after: http.Header{"User-Agent": {"a"}, "user-agent": {"b"}}, invalid: true},
		{name: "non ASCII", after: http.Header{"User-Agent": {"客户端"}}, invalid: true},
		{name: "tab", after: http.Header{"User-Agent": {"test\tagent"}}, invalid: true},
		{name: "newline", after: http.Header{"User-Agent": {"test\r\nX-Test: bad"}}, invalid: true},
		{name: "delete control", after: http.Header{"User-Agent": {"test\x7f"}}, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			before, after := test.before.Clone(), test.after.Clone()
			got, err := RequestUserAgentOverride(test.before, test.after)
			if test.invalid {
				if !errors.Is(err, ErrInvalidOutput) || got != nil || !strings.Contains(err.Error(), "request") {
					t.Fatalf("invalid override accepted: %v, %v", got, err)
				}
			} else if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("override = %v, %v; want %v", got, err, test.want)
			}
			if !reflect.DeepEqual(before, test.before) || !reflect.DeepEqual(after, test.after) {
				t.Fatal("caller-owned headers mutated")
			}
			if got != nil {
				if test.after == nil {
					test.after = make(http.Header)
				}
				test.after.Set("User-Agent", "later mutation")
				if *got != *test.want {
					t.Fatal("override shares mutable Header storage")
				}
			}
		})
	}
}
