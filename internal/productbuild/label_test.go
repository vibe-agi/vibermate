package productbuild

import (
	"runtime/debug"
	"testing"
)

func TestLabelUsesVersionThenBoundedVCSIdentity(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		info *debug.BuildInfo
		want string
	}{
		{"release", &debug.BuildInfo{Main: debug.Module{Version: "v1.2.3"}}, "v1.2.3"},
		{"revision", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "0123456789abcdef"}}}, "0123456789abcdef"},
		{"modified", &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "0123456789abcdef"}, {Key: "vcs.modified", Value: "true"}}}, "0123456789abcdef+modified"},
		{"unsafe", &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "secret\nvalue"}}}, development},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := label(test.info); got != test.want {
				t.Fatalf("label() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestLabelPrefersInjectedReleaseVersion(t *testing.T) {
	previous := releaseVersion
	releaseVersion = "v1.2.3"
	t.Cleanup(func() { releaseVersion = previous })
	if got := Label(); got != "v1.2.3" {
		t.Fatalf("Label() = %q", got)
	}
}
