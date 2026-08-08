package environment

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"sort"
	"strconv"
	"strings"
)

var ErrLaunchAuthorityRestartRequired = errors.New(
	"Capture must restart before the Environment can expand its launch authority",
)

// LaunchAuthorityDigest identifies the immutable Root/origin and credential
// mutation boundary delivered to one Capture at launch. It contains no secret.
type LaunchAuthorityDigest [sha256.Size]byte

func (digest LaunchAuthorityDigest) String() string { return hex.EncodeToString(digest[:]) }

// LaunchAuthorityBoundary is the durable upper bound of authority delivered
// to one Capture. Canonical JSON is kept as a comparable value so Assignment
// CAS and commit reconciliation cannot accidentally compare slice aliases.
type LaunchAuthorityBoundary struct {
	initialEnvironmentID       EnvironmentID
	initialEnvironmentRevision Revision
	initialEnvironmentDigest   CandidateDigest
	protectedJSON              string
	managedJSON                string
	digest                     LaunchAuthorityDigest
}

func NewLaunchAuthorityBoundary(snapshot EnvironmentSnapshot) (LaunchAuthorityBoundary, error) {
	if snapshot.ID() == "" || snapshot.Revision() == 0 || snapshot.Revision() > MaxRevision ||
		snapshot.Digest() == (CandidateDigest{}) || snapshot.State() != StateActive {
		return LaunchAuthorityBoundary{}, ErrInvalidEnvironment
	}
	protected, managed, err := snapshotAuthorityScopes(snapshot)
	if err != nil {
		return LaunchAuthorityBoundary{}, err
	}
	return newLaunchAuthorityBoundary(
		snapshot.ID(), snapshot.Revision(), snapshot.Digest(), protected, managed,
	)
}

// NewLaunchAuthorityBoundaryFromScopes reconstructs trusted authority evidence
// at non-SQL composition seams and in fixtures. Production Capture creation
// derives the same value from an EnvironmentSnapshot under its publish gate.
func NewLaunchAuthorityBoundaryFromScopes(
	initialID EnvironmentID,
	initialRevision Revision,
	initialDigest CandidateDigest,
	protected []string,
	managed []string,
) (LaunchAuthorityBoundary, error) {
	return newLaunchAuthorityBoundary(
		initialID, initialRevision, initialDigest, protected, managed,
	)
}

// RestoreLaunchAuthorityBoundary reconstructs persisted, non-secret launch
// authority. Every byte is revalidated and its digest is recomputed.
func RestoreLaunchAuthorityBoundary(
	initialID EnvironmentID,
	initialRevision Revision,
	initialDigest CandidateDigest,
	protected []string,
	managed []string,
	digest LaunchAuthorityDigest,
) (LaunchAuthorityBoundary, error) {
	boundary, err := newLaunchAuthorityBoundary(
		initialID, initialRevision, initialDigest, protected, managed,
	)
	if err != nil || boundary.digest != digest {
		return LaunchAuthorityBoundary{}, ErrInvalidEnvironment
	}
	return boundary, nil
}

func newLaunchAuthorityBoundary(
	initialID EnvironmentID,
	initialRevision Revision,
	initialDigest CandidateDigest,
	protected []string,
	managed []string,
) (LaunchAuthorityBoundary, error) {
	if _, err := NewEnvironmentID(initialID.String()); err != nil ||
		initialRevision == 0 || initialRevision > MaxRevision ||
		initialDigest == (CandidateDigest{}) {
		return LaunchAuthorityBoundary{}, ErrInvalidEnvironment
	}
	protected, protectedSet, err := canonicalLaunchAuthorities(protected)
	if err != nil {
		return LaunchAuthorityBoundary{}, err
	}
	managed, _, err = canonicalLaunchAuthorities(managed)
	if err != nil {
		return LaunchAuthorityBoundary{}, err
	}
	for _, authority := range managed {
		if _, exists := protectedSet[authority]; !exists {
			return LaunchAuthorityBoundary{}, ErrInvalidEnvironment
		}
	}
	protectedJSON, err := json.Marshal(protected)
	if err != nil {
		return LaunchAuthorityBoundary{}, err
	}
	managedJSON, err := json.Marshal(managed)
	if err != nil {
		return LaunchAuthorityBoundary{}, err
	}
	boundary := LaunchAuthorityBoundary{
		initialEnvironmentID:       initialID,
		initialEnvironmentRevision: initialRevision,
		initialEnvironmentDigest:   initialDigest,
		protectedJSON:              string(protectedJSON),
		managedJSON:                string(managedJSON),
	}
	boundary.digest = launchAuthorityDigest(boundary)
	return boundary, nil
}

func (boundary LaunchAuthorityBoundary) InitialEnvironmentID() EnvironmentID {
	return boundary.initialEnvironmentID
}

func (boundary LaunchAuthorityBoundary) InitialEnvironmentRevision() Revision {
	return boundary.initialEnvironmentRevision
}

func (boundary LaunchAuthorityBoundary) InitialEnvironmentDigest() CandidateDigest {
	return boundary.initialEnvironmentDigest
}

func (boundary LaunchAuthorityBoundary) Digest() LaunchAuthorityDigest { return boundary.digest }

func (boundary LaunchAuthorityBoundary) ProtectedAuthorities() []string {
	values, _ := decodeLaunchAuthorities(boundary.protectedJSON)
	return values
}

func (boundary LaunchAuthorityBoundary) ManagedCredentialAuthorities() []string {
	values, _ := decodeLaunchAuthorities(boundary.managedJSON)
	return values
}

func (boundary LaunchAuthorityBoundary) Validate() error {
	restored, err := RestoreLaunchAuthorityBoundary(
		boundary.initialEnvironmentID,
		boundary.initialEnvironmentRevision,
		boundary.initialEnvironmentDigest,
		boundary.ProtectedAuthorities(),
		boundary.ManagedCredentialAuthorities(),
		boundary.digest,
	)
	if err != nil || restored != boundary {
		return ErrInvalidEnvironment
	}
	return nil
}

// Covers reports whether a later Environment snapshot stays within both
// launch-time boundaries. A new protected origin could receive the delivered
// Root; a new managed origin could have a client credential stripped. Either
// change therefore requires a new Capture grant rather than a hot switch.
func (boundary LaunchAuthorityBoundary) Covers(snapshot EnvironmentSnapshot) error {
	if boundary.Validate() != nil {
		return ErrInvalidEnvironment
	}
	protected, managed, err := snapshotAuthorityScopes(snapshot)
	if err != nil {
		return err
	}
	permittedProtected := boundary.ProtectedAuthorities()
	permittedManaged := boundary.ManagedCredentialAuthorities()
	if !authoritySubset(protected, permittedProtected) ||
		!sameCredentialRewriteForRetainedAuthorities(protected, managed, permittedManaged) {
		return ErrLaunchAuthorityRestartRequired
	}
	return nil
}

func snapshotAuthorityScopes(snapshot EnvironmentSnapshot) ([]string, []string, error) {
	if snapshot.ID() == "" || snapshot.Revision() == 0 ||
		snapshot.Digest() == (CandidateDigest{}) ||
		(snapshot.State() != StateActive && snapshot.State() != StateDisabled) {
		return nil, nil, ErrInvalidEnvironment
	}
	endpoints := snapshot.ClientEndpoints()
	protected := make([]string, 0, len(endpoints))
	managed := make([]string, 0, len(endpoints))
	for _, endpoint := range endpoints {
		origin := endpoint.ClientOrigin()
		if origin.Validate() != nil {
			return nil, nil, ErrInvalidEnvironment
		}
		authority := origin.EndpointAuthority()
		protected = append(protected, authority)
		if endpointUsesOnlyManagedAccounts(endpoint) {
			managed = append(managed, authority)
		}
	}
	protected, _, err := canonicalLaunchAuthorities(protected)
	if err != nil {
		return nil, nil, err
	}
	managed, _, err = canonicalLaunchAuthorities(managed)
	if err != nil {
		return nil, nil, err
	}
	return protected, managed, nil
}

func endpointUsesOnlyManagedAccounts(endpoint ClientEndpointSnapshot) bool {
	// Credential environment rewriting is authority-wide, while route auth is
	// still injected by Core per attempt. A mixed endpoint deliberately keeps
	// the client's ambient credential so passthrough remains usable; managed
	// attempts cannot leak it because their typed AuthDriver overwrites client
	// authentication at the final outbound step.
	plans := endpoint.ProtocolPlans()
	if len(plans) == 0 {
		return false
	}
	for _, plan := range plans {
		if len(plan.UpstreamPlan.Routes) == 0 {
			return false
		}
		for _, route := range plan.UpstreamPlan.Routes {
			if route.AccountPolicy.Mode != AccountModeManaged {
				return false
			}
		}
	}
	return true
}

func canonicalLaunchAuthorities(values []string) ([]string, map[string]struct{}, error) {
	values = append([]string{}, values...)
	seen := make(map[string]struct{}, len(values))
	for _, authority := range values {
		host, port, err := net.SplitHostPort(authority)
		if err != nil || host == "" || strings.ToLower(host) != host ||
			strings.TrimSpace(authority) != authority {
			return nil, nil, ErrInvalidEnvironment
		}
		number, err := strconv.ParseUint(port, 10, 16)
		if err != nil || number == 0 {
			return nil, nil, ErrInvalidEnvironment
		}
		if _, duplicate := seen[authority]; duplicate {
			return nil, nil, ErrInvalidEnvironment
		}
		seen[authority] = struct{}{}
	}
	sort.Strings(values)
	return values, seen, nil
}

func decodeLaunchAuthorities(encoded string) ([]string, error) {
	var values []string
	if err := json.Unmarshal([]byte(encoded), &values); err != nil || values == nil {
		return nil, ErrInvalidEnvironment
	}
	canonical, _, err := canonicalLaunchAuthorities(values)
	if err != nil {
		return nil, err
	}
	reencoded, err := json.Marshal(canonical)
	if err != nil || string(reencoded) != encoded {
		return nil, ErrInvalidEnvironment
	}
	return canonical, nil
}

func authoritySubset(candidate, permitted []string) bool {
	allowed := make(map[string]struct{}, len(permitted))
	for _, value := range permitted {
		allowed[value] = struct{}{}
	}
	for _, value := range candidate {
		if _, exists := allowed[value]; !exists {
			return false
		}
	}
	return true
}

func sameCredentialRewriteForRetainedAuthorities(
	protected []string,
	managed []string,
	permittedManaged []string,
) bool {
	candidateManaged := make(map[string]struct{}, len(managed))
	for _, value := range managed {
		candidateManaged[value] = struct{}{}
	}
	launchManaged := make(map[string]struct{}, len(permittedManaged))
	for _, value := range permittedManaged {
		launchManaged[value] = struct{}{}
	}
	for _, authority := range protected {
		_, candidateUsesRewrite := candidateManaged[authority]
		_, launchUsesRewrite := launchManaged[authority]
		if candidateUsesRewrite != launchUsesRewrite {
			return false
		}
	}
	return true
}

func launchAuthorityDigest(boundary LaunchAuthorityBoundary) LaunchAuthorityDigest {
	hash := sha256.New()
	hash.Write([]byte("vibermate/environment/launch-authority\x00"))
	hash.Write([]byte(boundary.initialEnvironmentID.String()))
	hash.Write([]byte{0})
	var revision [8]byte
	binary.BigEndian.PutUint64(revision[:], uint64(boundary.initialEnvironmentRevision))
	hash.Write(revision[:])
	digest := boundary.initialEnvironmentDigest
	hash.Write(digest[:])
	hash.Write([]byte(boundary.protectedJSON))
	hash.Write([]byte{0})
	hash.Write([]byte(boundary.managedJSON))
	var result LaunchAuthorityDigest
	copy(result[:], hash.Sum(nil))
	return result
}
