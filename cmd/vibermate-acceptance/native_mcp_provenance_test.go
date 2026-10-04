package main

import (
	"errors"
	"runtime/debug"
	"strings"
	"testing"
)

func TestNativeBuildEvidenceRequiresActualTagAndAdapterAgreement(t *testing.T) {
	for _, test := range []struct {
		tags          string
		adapter, want bool
	}{
		{"", false, true}, {nativeSyntheticTag, true, true}, {nativeSyntheticTag, false, false}, {"", true, false}, {"other," + nativeSyntheticTag, true, true},
	} {
		e, err := nativeEvidenceFromBuild(&debug.BuildInfo{GoVersion: "go1.26.8", Settings: []debug.BuildSetting{{Key: "-tags", Value: test.tags}}}, test.adapter)
		if (err == nil) != test.want {
			t.Fatalf("tags=%q adapter=%t evidence=%+v err=%v", test.tags, test.adapter, e, err)
		}
		if err == nil && (e.Scope != "synthetic_private_runtime" || e.GoVersion != "go1.26.8" || test.adapter && (e.SyntheticProfileIdentifier != "synthetic_read/v1" || e.ProfileDefinitionDigest != "9ec74da54cc7f865d22dc1d020abf5a6d9a7df1cd7a1db07d509c6394329065f") || !test.adapter && (e.SyntheticProfileIdentifier != "" || e.ProfileDefinitionDigest != "")) {
			t.Fatal("build scope/profile identity changed")
		}
	}
	if _, err := nativeEvidenceFromBuild(nil, false); err == nil {
		t.Fatal("missing build info admitted")
	}
}

func TestNativeReportArtifactTamperingCannotRetainPassedRows(t *testing.T) {
	_, available := nativeAcceptanceContentProfiles()
	tags := ""
	if available {
		tags = nativeSyntheticTag
	}
	actual, err := nativeEvidenceFromBuild(&debug.BuildInfo{GoVersion: "go1.26.8", Settings: []debug.BuildSetting{{Key: "-tags", Value: tags}}}, available)
	if err != nil {
		t.Fatal(err)
	}
	actual.ExecutableSHA256 = strings.Repeat("a", 64)
	for _, field := range []string{"valid", "scope", "profile", "definition", "tags", "go", "artifact", "unavailable"} {
		t.Run(field, func(t *testing.T) {
			report := newNativeMCPReport()
			report.nativeArtifactEvidence = actual
			report.Cases[12].Outcome = "passed"
			report.Cases[12].NativeOutbound = 5
			var evidenceErr error
			switch field {
			case "scope":
				report.Scope = "production"
			case "profile":
				report.SyntheticProfileIdentifier = "other"
			case "definition":
				report.ProfileDefinitionDigest = strings.Repeat("b", 64)
			case "tags":
				report.GoBuildTags = "wrong"
			case "go":
				report.GoVersion = "go0"
			case "artifact":
				report.ExecutableSHA256 = strings.Repeat("b", 64)
			case "unavailable":
				evidenceErr = errors.New("artifact unavailable")
			}
			bad := validateNativeArtifactCases(&report, actual, evidenceErr)
			if field == "valid" {
				if bad || report.Cases[12].Outcome != "passed" {
					t.Fatal("valid actual artifact dropped")
				}
			} else if !bad || report.Cases[12].Outcome != "incomplete" || report.Cases[12].Reason != "artifact_provenance_unavailable" || report.Cases[12].NativeOutbound != 5 {
				t.Fatal("tampered artifact retained passed evidence or erased counts")
			}
		})
	}
}
