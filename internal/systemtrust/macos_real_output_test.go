package systemtrust

import (
	"context"
	"crypto/sha1" // #nosec G505 -- Apple's trust-list lookup key is SHA-1.
	"encoding/hex"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type currentUserObservationExecutor struct {
	calls []CommandSpec
}

func (executor *currentUserObservationExecutor) Execute(
	_ context.Context,
	spec CommandSpec,
) (CommandResult, error) {
	executor.calls = append(executor.calls, spec)
	switch spec.Kind() {
	case CommandInspectExactPresence:
		return NewCommandResult(CommandOutcomeSucceeded, nil, nil)
	case CommandInspectUserTrust:
		return NewCommandResult(
			CommandOutcomeFailed,
			nil,
			[]byte("SecTrustSettingsCreateExternalRepresentation: No Trust Settings were found.\n"),
		)
	default:
		return CommandResult{}, ErrCommandInvalid
	}
}

func TestProductionObservationReadsOnlyTheCurrentUserTrustScope(t *testing.T) {
	root := testPublicRoot(t)
	executor := &currentUserObservationExecutor{}
	adapter, err := NewProductionMacOSAdapter(executor)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := adapter.inspect(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Target() != MacOSCurrentUserTarget() ||
		observation.EvidenceRevision() != EvidenceRevisionMacOSSecurityV2 ||
		observation.Presence() != ExactPresenceAbsent ||
		observation.TrustDecision() != TrustDecisionUntrusted {
		t.Fatalf("unexpected current-user observation: %+v", observation)
	}
	if len(executor.calls) != 2 {
		t.Fatalf("unexpected observation commands: %v", executor.calls)
	}
	if arguments := executor.calls[0].Arguments(); !slices.Equal(arguments, []string{
		"find-certificate", "-a", "-c", viberMateRootCommonName, "-p",
	}) {
		t.Fatalf("certificate lookup escaped the current user's search list: %v", arguments)
	}
	trustArguments := executor.calls[1].Arguments()
	if len(trustArguments) != 2 || trustArguments[0] != "trust-settings-export" ||
		filepath.Base(trustArguments[1]) != "user-trust.plist" {
		t.Fatalf("trust lookup escaped the current user domain: %v", trustArguments)
	}
}

func TestMacOSTrustExportBindsDecisionToTheExactCertificate(t *testing.T) {
	root := testPublicRoot(t)
	exactKey := strings.ToUpper(hex.EncodeToString(sha1Digest(root.certificateDER)))
	trustedSetting := `<key>trustSettings</key><array><dict>` +
		`<key>kSecTrustSettingsPolicyName</key><string>sslServer</string>` +
		`<key>kSecTrustSettingsResult</key><integer>1</integer>` +
		`</dict></array>`

	decision, err := parseMacOSExportTrustDecision(
		trustExport(exactKey, `<dict>`+trustedSetting+`</dict>`),
		root,
	)
	if err != nil || decision != TrustDecisionTrusted {
		t.Fatalf("exact trusted entry was not accepted: decision=%s error=%v", decision, err)
	}

	// A same-name certificate can coexist in the keychain. The export's exact
	// SHA-1 key, rather than display metadata, must decide which Root is trusted.
	decision, err = parseMacOSExportTrustDecision(
		trustExport("0000000000000000000000000000000000000000", `<dict>`+trustedSetting+`</dict>`),
		root,
	)
	if err != nil || decision != TrustDecisionUntrusted {
		t.Fatalf("foreign entry affected exact Root: decision=%s error=%v", decision, err)
	}
}

func TestMacOSTrustExportRequiresExplicitSSLTrust(t *testing.T) {
	root := testPublicRoot(t)
	exactKey := strings.ToUpper(hex.EncodeToString(sha1Digest(root.certificateDER)))
	cases := []struct {
		name     string
		entry    string
		decision TrustDecision
	}{
		{name: "certificate object without settings", entry: `<dict></dict>`, decision: TrustDecisionUntrusted},
		{
			name:     "empty settings means trust for every use",
			entry:    `<dict><key>trustSettings</key><array></array></dict>`,
			decision: TrustDecisionTrusted,
		},
		{
			name: "missing result defaults to trust root",
			entry: `<dict><key>trustSettings</key><array><dict>` +
				`<key>kSecTrustSettingsPolicyName</key><string>sslServer</string>` +
				`</dict></array></dict>`,
			decision: TrustDecisionTrusted,
		},
		{
			name: "non SSL policy",
			entry: `<dict><key>trustSettings</key><array><dict>` +
				`<key>kSecTrustSettingsPolicyName</key><string>SMIME</string>` +
				`<key>kSecTrustSettingsResult</key><integer>1</integer>` +
				`</dict></array></dict>`,
			decision: TrustDecisionUntrusted,
		},
		{
			name: "explicit SSL deny",
			entry: `<dict><key>trustSettings</key><array><dict>` +
				`<key>kSecTrustSettingsPolicyName</key><string>sslServer</string>` +
				`<key>kSecTrustSettingsResult</key><integer>3</integer>` +
				`</dict></array></dict>`,
			decision: TrustDecisionUntrusted,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			decision, err := parseMacOSExportTrustDecision(
				trustExport(exactKey, test.entry),
				root,
			)
			if err != nil || decision != test.decision {
				t.Fatalf("decision=%s error=%v", decision, err)
			}
		})
	}
}

func TestMacOSTrustExportRecognizesOnlyTheCanonicalEmptyDomainFailure(t *testing.T) {
	empty, err := NewCommandResult(
		CommandOutcomeFailed,
		nil,
		[]byte("SecTrustSettingsCreateExternalRepresentation: No Trust Settings were found.\n"),
	)
	if err != nil || !trustSettingsExportIsEmpty(empty) {
		t.Fatalf("canonical empty domain was not recognized: %v", err)
	}
	permission, err := NewCommandResult(
		CommandOutcomeFailed,
		nil,
		[]byte("SecTrustSettingsCreateExternalRepresentation: User interaction is not allowed.\n"),
	)
	if err != nil || trustSettingsExportIsEmpty(permission) {
		t.Fatalf("unrelated failure was accepted as an empty domain: %v", err)
	}
}

type emptyUserTrustExecutor struct{}

func (emptyUserTrustExecutor) Execute(
	_ context.Context,
	spec CommandSpec,
) (CommandResult, error) {
	if spec.Kind() != CommandInspectUserTrust || !spec.Valid() {
		return CommandResult{}, ErrCommandInvalid
	}
	return NewCommandResult(
		CommandOutcomeFailed,
		nil,
		[]byte("SecTrustSettingsCreateExternalRepresentation: No Trust Settings were found.\n"),
	)
}

func TestProductionTrustObservationTreatsAnEmptyUserDomainAsUntrusted(t *testing.T) {
	root := testPublicRoot(t)
	adapter, err := NewProductionMacOSAdapter(emptyUserTrustExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := adapter.inspectTrustDecision(context.Background(), root)
	if err != nil || decision != TrustDecisionUntrusted {
		t.Fatalf("empty user domain decision=%s error=%v", decision, err)
	}
}

func TestMacOSTrustExportFailsClosedOnAmbiguousOrMalformedExactEntry(t *testing.T) {
	root := testPublicRoot(t)
	exactKey := strings.ToUpper(hex.EncodeToString(sha1Digest(root.certificateDER)))
	trusted := `<dict><key>trustSettings</key><array><dict>` +
		`<key>kSecTrustSettingsPolicyName</key><string>sslServer</string>` +
		`<key>kSecTrustSettingsResult</key><integer>1</integer>` +
		`</dict></array></dict>`
	cases := [][]byte{
		[]byte(`<plist version="1.0"><dict></plist>`),
		trustExport(exactKey, `<string>not-a-dictionary</string>`),
		[]byte(`<plist version="1.0"><dict><key>trustList</key><dict><key>` + exactKey +
			`</key>` + trusted + `<key>` + exactKey + `</key>` + trusted +
			`</dict></dict></plist>`),
	}
	for index, output := range cases {
		decision, err := parseMacOSExportTrustDecision(output, root)
		if !errors.Is(err, ErrObservationUnknown) || decision != TrustDecisionUnknown {
			t.Fatalf("case %d did not fail closed: decision=%s error=%v", index, decision, err)
		}
	}
}

func trustExport(key, entry string) []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>` +
		`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" ` +
		`"http://www.apple.com/DTDs/PropertyList-1.0.dtd">` +
		`<plist version="1.0"><dict><key>trustList</key><dict><key>` +
		key + `</key>` + entry + `</dict></dict></plist>`)
}

func sha1Digest(value []byte) []byte {
	digest := sha1.Sum(value) // #nosec G401 -- exact key used by macOS plist.
	return digest[:]
}
