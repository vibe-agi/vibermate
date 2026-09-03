package systemtrust

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	macOSSecurityExecutable = "/usr/bin/security"
	viberMateRootCommonName = "ViberMate Local Root"
	maxCommandOutputBytes   = 64 << 10
	maxFixtureCertificates  = 256
	maxFixtureTrustEntries  = 256
	trustFixtureSchemaV1    = "vibermate-macos-user-trust-fixture-v1"
)

type CommandKind string

const (
	CommandInspectExactPresence CommandKind = "inspect_exact_presence"
	CommandInspectUserTrust     CommandKind = "inspect_user_trust"
	CommandEnsureExactTrust     CommandKind = "ensure_exact_trust"
	CommandRemoveExactTrust     CommandKind = "remove_exact_trust"
	CommandDeleteExactObject    CommandKind = "delete_exact_object"
)

// CommandSpec is constructed only by the macOS adapter. Its argument copy is
// available to the injected executor but never enters an operation result.
type CommandSpec struct {
	kind       CommandKind
	executable string
	arguments  []string
}

func (spec CommandSpec) Kind() CommandKind {
	return spec.kind
}

func (spec CommandSpec) Executable() string {
	return spec.executable
}

func (spec CommandSpec) Arguments() []string {
	return append([]string(nil), spec.arguments...)
}

// Valid reports whether this command was constructed by the bounded macOS
// adapter. Production executors may use this check without gaining access to
// the command's mutable internals.
func (spec CommandSpec) Valid() bool {
	return spec.valid()
}

func (spec CommandSpec) valid() bool {
	if spec.executable != macOSSecurityExecutable || len(spec.arguments) == 0 {
		return false
	}
	switch spec.kind {
	case CommandInspectExactPresence:
		return slicesEqual(spec.arguments, []string{
			"find-certificate",
			"-a",
			"-c",
			viberMateRootCommonName,
			"-p",
		})
	case CommandInspectUserTrust:
		return slicesEqual(
			spec.arguments,
			[]string{"dump-trust-settings"},
		) || (len(spec.arguments) == 2 &&
			spec.arguments[0] == "trust-settings-export" &&
			validTrustSettingsArtifactPath(spec.arguments[1]))
	case CommandEnsureExactTrust:
		return len(spec.arguments) == 6 &&
			slicesEqual(spec.arguments[:5], []string{
				"add-trusted-cert",
				"-r",
				"trustRoot",
				"-p",
				"ssl",
			}) && validCertificateArtifactPath(spec.arguments[5])
	case CommandRemoveExactTrust:
		return len(spec.arguments) == 2 &&
			spec.arguments[0] == "remove-trusted-cert" &&
			validCertificateArtifactPath(spec.arguments[1])
	case CommandDeleteExactObject:
		return len(spec.arguments) == 3 &&
			spec.arguments[0] == "delete-certificate" &&
			spec.arguments[1] == "-Z" &&
			validUpperSHA256(spec.arguments[2])
	default:
		return false
	}
}

func slicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func validCertificateArtifactPath(path string) bool {
	temporaryRoot := strings.TrimRight(
		os.TempDir(),
		string(os.PathSeparator),
	) + string(os.PathSeparator)
	return filepath.IsAbs(path) && filepath.Base(path) == "root.cer" &&
		strings.HasPrefix(filepath.Dir(path)+string(os.PathSeparator), temporaryRoot)
}

func validTrustSettingsArtifactPath(path string) bool {
	temporaryRoot := strings.TrimRight(
		os.TempDir(),
		string(os.PathSeparator),
	) + string(os.PathSeparator)
	return filepath.IsAbs(path) && filepath.Base(path) == "user-trust.plist" &&
		strings.HasPrefix(filepath.Dir(path)+string(os.PathSeparator), temporaryRoot)
}

func validUpperSHA256(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToUpper(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

type CommandOutcome string

const (
	CommandOutcomeSucceeded        CommandOutcome = "succeeded"
	CommandOutcomeUserCancelled    CommandOutcome = "user_cancelled"
	CommandOutcomePermissionDenied CommandOutcome = "permission_denied"
	CommandOutcomeTimedOut         CommandOutcome = "timed_out"
	CommandOutcomeFailed           CommandOutcome = "failed"
	CommandOutcomeIndeterminate    CommandOutcome = "indeterminate"
)

func (outcome CommandOutcome) valid() bool {
	switch outcome {
	case CommandOutcomeSucceeded,
		CommandOutcomeUserCancelled,
		CommandOutcomePermissionDenied,
		CommandOutcomeTimedOut,
		CommandOutcomeFailed,
		CommandOutcomeIndeterminate:
		return true
	default:
		return false
	}
}

// CommandResult owns bounded copies of raw command evidence. Only the adapter
// can consume those bytes; they have no exported accessor.
type CommandResult struct {
	outcome CommandOutcome
	stdout  []byte
	stderr  []byte
}

func NewCommandResult(
	outcome CommandOutcome,
	stdout []byte,
	stderr []byte,
) (CommandResult, error) {
	if !outcome.valid() || len(stdout) > maxCommandOutputBytes ||
		len(stderr) > maxCommandOutputBytes {
		return CommandResult{}, ErrCommandInvalid
	}
	return CommandResult{
		outcome: outcome,
		stdout:  bytes.Clone(stdout),
		stderr:  bytes.Clone(stderr),
	}, nil
}

func (result CommandResult) Outcome() CommandOutcome {
	return result.outcome
}

func (result CommandResult) valid() bool {
	return result.outcome.valid() && len(result.stdout) <= maxCommandOutputBytes &&
		len(result.stderr) <= maxCommandOutputBytes
}

// CommandExecutor is an injected process boundary. An executor must report
// capture overflow as an error or indeterminate outcome; it must not truncate
// evidence and report success. This package intentionally provides no
// operating-system implementation.
type CommandExecutor interface {
	Execute(context.Context, CommandSpec) (CommandResult, error)
}

// MacOSAdapter maps fixed typed operations to bounded security command shapes.
// Construction fixes whether observations came from deterministic fixtures or
// the production security executable.
type MacOSAdapter struct {
	executor CommandExecutor
	evidence EvidenceRevision
}

func NewMacOSAdapter(executor CommandExecutor) (*MacOSAdapter, error) {
	if executor == nil {
		return nil, ErrCommandInvalid
	}
	return &MacOSAdapter{
		executor: executor,
		evidence: EvidenceRevisionMacOSFixtureV1,
	}, nil
}

func NewProductionMacOSAdapter(executor CommandExecutor) (*MacOSAdapter, error) {
	adapter, err := NewMacOSAdapter(executor)
	if err != nil {
		return nil, err
	}
	adapter.evidence = EvidenceRevisionMacOSSecurityV2
	return adapter, nil
}

func (adapter *MacOSAdapter) inspect(
	ctx context.Context,
	root publicRoot,
) (Observation, error) {
	evidence := EvidenceRevisionMacOSFixtureV1
	if adapter != nil && adapter.evidence.valid() {
		evidence = adapter.evidence
	}
	unknown := newObservation(
		root,
		ExactPresenceUnknown,
		TrustDecisionUnknown,
		evidence,
	)
	if adapter == nil || adapter.executor == nil || ctx == nil || !root.valid() {
		return unknown, ErrObservationUnknown
	}
	certificate, err := x509.ParseCertificate(root.certificateDER)
	if err != nil || certificate.Subject.CommonName != viberMateRootCommonName {
		return unknown, ErrObservationUnknown
	}
	presenceResult, err := adapter.run(ctx, CommandSpec{
		kind:       CommandInspectExactPresence,
		executable: macOSSecurityExecutable,
		arguments: []string{
			"find-certificate",
			"-a",
			"-c",
			viberMateRootCommonName,
			"-p",
		},
	})
	if err != nil || presenceResult.outcome != CommandOutcomeSucceeded {
		return unknown, errors.Join(ErrObservationUnknown, err)
	}
	presence, err := parseExactPresence(
		presenceResult.stdout,
		root.identity.Digest().String(),
	)
	if err != nil {
		return unknown, errors.Join(ErrObservationUnknown, err)
	}
	decision, err := adapter.inspectTrustDecision(ctx, root)
	if err != nil {
		return unknown, errors.Join(ErrObservationUnknown, err)
	}
	observation := newObservation(
		root,
		presence,
		decision,
		adapter.evidence,
	)
	if !observation.Valid() {
		return unknown, ErrObservationUnknown
	}
	return observation, nil
}

func (adapter *MacOSAdapter) inspectTrustDecision(
	ctx context.Context,
	root publicRoot,
) (TrustDecision, error) {
	if adapter == nil || ctx == nil || !root.valid() {
		return TrustDecisionUnknown, ErrObservationUnknown
	}
	if adapter.evidence != EvidenceRevisionMacOSSecurityV2 {
		result, err := adapter.run(ctx, CommandSpec{
			kind:       CommandInspectUserTrust,
			executable: macOSSecurityExecutable,
			arguments:  []string{"dump-trust-settings"},
		})
		if err != nil || result.outcome != CommandOutcomeSucceeded {
			return TrustDecisionUnknown, errors.Join(ErrObservationUnknown, err)
		}
		return parseTrustDecision(result.stdout, root)
	}

	path, cleanup, err := prepareTrustSettingsExport()
	if err != nil {
		return TrustDecisionUnknown, err
	}
	defer cleanup()
	result, err := adapter.run(ctx, CommandSpec{
		kind:       CommandInspectUserTrust,
		executable: macOSSecurityExecutable,
		arguments:  []string{"trust-settings-export", path},
	})
	if err == nil && trustSettingsExportIsEmpty(result) {
		return TrustDecisionUntrusted, nil
	}
	if err != nil || result.outcome != CommandOutcomeSucceeded {
		return TrustDecisionUnknown, errors.Join(ErrObservationUnknown, err)
	}
	exported, err := readTrustSettingsExport(path)
	if err != nil {
		return TrustDecisionUnknown, err
	}
	return parseMacOSExportTrustDecision(exported, root)
}

func trustSettingsExportIsEmpty(result CommandResult) bool {
	return result.valid() && result.outcome == CommandOutcomeFailed &&
		len(result.stdout) == 0 &&
		string(result.stderr) ==
			"SecTrustSettingsCreateExternalRepresentation: No Trust Settings were found.\n"
}

func prepareTrustSettingsExport() (string, func(), error) {
	directory, err := os.MkdirTemp("", "vibermate-user-trust-")
	if err != nil {
		return "", nil, ErrCommandInvalid
	}
	cleanup := func() {
		_ = os.RemoveAll(directory)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		cleanup()
		return "", nil, ErrCommandInvalid
	}
	return filepath.Join(directory, "user-trust.plist"), cleanup, nil
}

func readTrustSettingsExport(path string) ([]byte, error) {
	if !validTrustSettingsArtifactPath(path) {
		return nil, ErrObservationUnknown
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 ||
		!info.Mode().IsRegular() || info.Mode().Perm() != 0o600 ||
		info.Size() <= 0 || info.Size() > maxTrustSettingsExportBytes {
		return nil, errors.Join(ErrObservationUnknown, err)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.Join(ErrObservationUnknown, err)
	}
	defer file.Close()
	value, err := io.ReadAll(io.LimitReader(file, maxTrustSettingsExportBytes+1))
	if err != nil || len(value) == 0 || len(value) > maxTrustSettingsExportBytes {
		return nil, errors.Join(ErrObservationUnknown, err)
	}
	return value, nil
}

// parseTrustDecision accepts only the deterministic JSON grammar used by unit
// fixtures. Production uses the exact-certificate-keyed XML export parser.
func parseTrustDecision(output []byte, root publicRoot) (TrustDecision, error) {
	if len(output) == 0 || len(output) > maxCommandOutputBytes || !root.valid() {
		return TrustDecisionUnknown, ErrObservationUnknown
	}
	trimmed := bytes.TrimSpace(output)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return TrustDecisionUnknown, ErrObservationUnknown
	}
	return parseFixtureTrustDecision(
		trimmed,
		root.identity.Digest().String(),
	)
}

func (adapter *MacOSAdapter) mutate(
	ctx context.Context,
	step Step,
	root publicRoot,
) (CommandOutcome, error) {
	if adapter == nil || adapter.executor == nil || ctx == nil || !root.valid() ||
		!step.mutation() {
		return CommandOutcomeIndeterminate, ErrCommandInvalid
	}
	if err := ctx.Err(); err != nil {
		return CommandOutcomeIndeterminate, context.Cause(ctx)
	}
	var (
		spec    CommandSpec
		cleanup func()
		err     error
	)
	switch step {
	case StepEnsureExactCertificateAndUserTrust:
		var path string
		path, cleanup, err = materializeCertificate(root)
		if err == nil {
			spec = CommandSpec{
				kind:       CommandEnsureExactTrust,
				executable: macOSSecurityExecutable,
				arguments: []string{
					"add-trusted-cert",
					"-r",
					"trustRoot",
					"-p",
					"ssl",
					path,
				},
			}
		}
	case StepRemoveExactUserTrustSettings:
		var path string
		path, cleanup, err = materializeCertificate(root)
		if err == nil {
			spec = CommandSpec{
				kind:       CommandRemoveExactTrust,
				executable: macOSSecurityExecutable,
				arguments:  []string{"remove-trusted-cert", path},
			}
		}
	case StepDeleteExactCertificate:
		spec = CommandSpec{
			kind:       CommandDeleteExactObject,
			executable: macOSSecurityExecutable,
			arguments: []string{
				"delete-certificate",
				"-Z",
				strings.ToUpper(root.identity.Digest().String()),
			},
		}
	}
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil || !spec.valid() {
		return CommandOutcomeIndeterminate, errors.Join(ErrCommandInvalid, err)
	}
	result, runErr := adapter.run(ctx, spec)
	if runErr != nil {
		return CommandOutcomeIndeterminate, runErr
	}
	return result.outcome, nil
}

func (adapter *MacOSAdapter) run(
	ctx context.Context,
	spec CommandSpec,
) (result CommandResult, err error) {
	if adapter == nil || adapter.executor == nil || ctx == nil || !spec.valid() {
		return CommandResult{}, ErrCommandInvalid
	}
	defer func() {
		if recover() != nil {
			result = CommandResult{outcome: CommandOutcomeIndeterminate}
			err = ErrCommandInvalid
		}
	}()
	result, err = adapter.executor.Execute(ctx, spec)
	if err != nil || !result.valid() {
		return CommandResult{outcome: CommandOutcomeIndeterminate}, ErrCommandInvalid
	}
	return result, nil
}

func materializeCertificate(root publicRoot) (string, func(), error) {
	if !root.valid() {
		return "", nil, ErrCurrentRootInvalid
	}
	directory, err := os.MkdirTemp("", "vibermate-public-root-")
	if err != nil {
		return "", nil, ErrCommandInvalid
	}
	cleanup := func() {
		_ = os.RemoveAll(directory)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		cleanup()
		return "", nil, ErrCommandInvalid
	}
	path := filepath.Join(directory, "root.cer")
	if err := os.WriteFile(path, root.certificateDER, 0o600); err != nil {
		cleanup()
		return "", nil, ErrCommandInvalid
	}
	written, err := os.ReadFile(path)
	if err != nil || sha256.Sum256(written) != sha256.Sum256(root.certificateDER) {
		cleanup()
		return "", nil, ErrCommandInvalid
	}
	return path, cleanup, nil
}

func parseExactPresence(output []byte, expectedDigest string) (ExactPresence, error) {
	if len(output) > maxCommandOutputBytes {
		return ExactPresenceUnknown, ErrObservationUnknown
	}
	rest := bytes.TrimSpace(output)
	if len(rest) == 0 {
		return ExactPresenceAbsent, nil
	}
	found := 0
	certificates := 0
	for len(rest) != 0 {
		block, remaining := pem.Decode(rest)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Bytes) == 0 {
			return ExactPresenceUnknown, ErrObservationUnknown
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return ExactPresenceUnknown, ErrObservationUnknown
		}
		certificates++
		if certificates > maxFixtureCertificates {
			return ExactPresenceUnknown, ErrObservationUnknown
		}
		if fmt.Sprintf("%x", sha256.Sum256(block.Bytes)) == expectedDigest {
			found++
		}
		rest = bytes.TrimSpace(remaining)
	}
	switch found {
	case 0:
		return ExactPresenceAbsent, nil
	case 1:
		return ExactPresencePresent, nil
	default:
		return ExactPresenceUnknown, ErrObservationUnknown
	}
}

type trustFixture struct {
	Schema   string              `json:"schema"`
	Complete bool                `json:"complete"`
	Entries  []trustFixtureEntry `json:"entries"`
}

type trustFixtureEntry struct {
	DERDigest string `json:"derSha256"`
	Usage     string `json:"usage"`
	Decision  string `json:"decision"`
}

func parseFixtureTrustDecision(
	output []byte,
	expectedDigest string,
) (TrustDecision, error) {
	if len(output) == 0 || len(output) > maxCommandOutputBytes {
		return TrustDecisionUnknown, ErrObservationUnknown
	}
	if err := rejectDuplicateJSONFields(output); err != nil {
		return TrustDecisionUnknown, ErrObservationUnknown
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	var fixture trustFixture
	if err := decoder.Decode(&fixture); err != nil {
		return TrustDecisionUnknown, ErrObservationUnknown
	}
	if err := consumeJSONEOF(decoder); err != nil ||
		fixture.Schema != trustFixtureSchemaV1 || !fixture.Complete ||
		len(fixture.Entries) > maxFixtureTrustEntries {
		return TrustDecisionUnknown, ErrObservationUnknown
	}
	matched := false
	decision := TrustDecisionUntrusted
	for _, entry := range fixture.Entries {
		digest := strings.ToLower(entry.DERDigest)
		decodedDigest, decodeErr := hex.DecodeString(digest)
		if len(digest) != sha256.Size*2 || digest != entry.DERDigest ||
			decodeErr != nil || len(decodedDigest) != sha256.Size {
			return TrustDecisionUnknown, ErrObservationUnknown
		}
		if entry.Usage != string(TrustUsageServerTLS) {
			return TrustDecisionUnknown, ErrObservationUnknown
		}
		entryDecision := TrustDecision(entry.Decision)
		if entryDecision != TrustDecisionTrusted &&
			entryDecision != TrustDecisionUntrusted {
			return TrustDecisionUnknown, ErrObservationUnknown
		}
		if digest != expectedDigest {
			continue
		}
		if matched {
			return TrustDecisionUnknown, ErrObservationUnknown
		}
		matched = true
		decision = entryDecision
	}
	return decision, nil
}

func rejectDuplicateJSONFields(input []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(input))
	if err := consumeUniqueJSONValue(decoder, 0); err != nil {
		return err
	}
	return consumeJSONEOF(decoder)
}

func consumeUniqueJSONValue(decoder *json.Decoder, depth int) error {
	const maxFixtureJSONDepth = 16
	if depth > maxFixtureJSONDepth {
		return ErrObservationUnknown
	}
	token, err := decoder.Token()
	if err != nil {
		return ErrObservationUnknown
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			nameToken, nameErr := decoder.Token()
			name, ok := nameToken.(string)
			if nameErr != nil || !ok {
				return ErrObservationUnknown
			}
			if _, duplicate := seen[name]; duplicate {
				return ErrObservationUnknown
			}
			seen[name] = struct{}{}
			if err := consumeUniqueJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, closeErr := decoder.Token()
		if closeErr != nil || closing != json.Delim('}') {
			return ErrObservationUnknown
		}
		return nil
	case '[':
		for decoder.More() {
			if err := consumeUniqueJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, closeErr := decoder.Token()
		if closeErr != nil || closing != json.Delim(']') {
			return ErrObservationUnknown
		}
		return nil
	default:
		return ErrObservationUnknown
	}
}

func consumeJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return ErrObservationUnknown
}
