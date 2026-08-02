package codesignature

import (
	"errors"
	"fmt"
	"strings"
)

// A catalog used to hold the requirement expression itself, and the checks on
// it were `strings.Contains`. That meant a single string literal could satisfy
// every check while meaning something else entirely: `identifier "anchor apple
// subject.OU"` contains all three words and names one program nobody publishes.
// The catalogued entries were correct and reviewed, so nothing was exploitable
// — but the guarantee the comments claimed was not the guarantee the code
// provided, and a mistaken entry would have widened who receives the Root.
//
// So a requirement is no longer something a catalog can write. It is generated
// here from two validated fields, in one fixed shape, and there is no way to
// submit an expression.

// SigningIdentifier is the bundle or binary identifier a publisher signs
// under: `com.anthropic.claude-code`, `codex`.
type SigningIdentifier string

// TeamID is an Apple Developer team identifier: ten uppercase alphanumerics.
type TeamID string

var (
	// ErrInvalidIdentifier and ErrInvalidTeamID are separate so a catalog
	// mistake says which field is wrong.
	ErrInvalidIdentifier = errors.New("signing identifier is invalid")
	ErrInvalidTeamID     = errors.New("Apple team identifier is invalid")
)

const (
	// maxIdentifierBytes bounds a bundle identifier generously. Apple does not
	// publish a limit; this one is far above any real identifier and far below
	// anything that could be an expression.
	maxIdentifierBytes = 128
	// teamIDLength is fixed by Apple at ten characters.
	teamIDLength = 10
)

// Validate accepts only what a real identifier is made of.
//
// The character set is the point. A quote, a space, a bracket or an operator
// is what a smuggled expression needs, and none of them appear in a bundle
// identifier — so refusing them is not a heuristic filter, it is the set of
// characters the field actually uses.
func (identifier SigningIdentifier) Validate() error {
	value := string(identifier)
	if value == "" || len(value) > maxIdentifierBytes {
		return fmt.Errorf("%w: length %d", ErrInvalidIdentifier, len(value))
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		switch {
		case character >= 'a' && character <= 'z':
		case character >= 'A' && character <= 'Z':
		case character >= '0' && character <= '9':
		case character == '.' || character == '-' || character == '_':
		default:
			return fmt.Errorf(
				"%w: byte %d is not permitted",
				ErrInvalidIdentifier,
				index,
			)
		}
	}
	// A leading or trailing separator is not an identifier anyone publishes,
	// and an empty label between dots is the shape a parser might read
	// differently from a reader.
	if strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") ||
		strings.Contains(value, "..") {
		return fmt.Errorf("%w: malformed label structure", ErrInvalidIdentifier)
	}
	return nil
}

func (team TeamID) Validate() error {
	value := string(team)
	if len(value) != teamIDLength {
		return fmt.Errorf(
			"%w: length %d, want %d",
			ErrInvalidTeamID,
			len(value),
			teamIDLength,
		)
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		switch {
		case character >= 'A' && character <= 'Z':
		case character >= '0' && character <= '9':
		default:
			return fmt.Errorf(
				"%w: byte %d is not permitted",
				ErrInvalidTeamID,
				index,
			)
		}
	}
	return nil
}

// Developer ID certificate extension OIDs. The first marks the Developer ID
// intermediate, the second the Developer ID Application leaf. Together they
// are what distinguishes a Developer ID signature from any other signature
// Apple's root ever anchored, including the operating system's own binaries.
const (
	developerIDIntermediateOID = "1.2.840.113635.100.6.2.6"
	developerIDLeafOID         = "1.2.840.113635.100.6.1.13"
)

// Requirement is a platform requirement expression. It cannot be constructed
// from outside this package: the only way to obtain one is
// DeveloperIDRequirement, which builds the single shape this product accepts.
type Requirement struct {
	expression string
}

// DeveloperIDRequirement returns the requirement for one Developer ID
// publisher's program.
//
// The shape is fixed here and not negotiable by a caller. Every clause earns
// its place: the identifier names the program, so a publisher's other programs
// do not match; the anchor ties it to Apple's root; the two extensions make it
// a Developer ID signature specifically; the team names the publisher. Drop
// any one and the requirement means something wider than ADR-0016 allows.
func DeveloperIDRequirement(
	identifier SigningIdentifier,
	team TeamID,
) (Requirement, error) {
	if err := identifier.Validate(); err != nil {
		return Requirement{}, err
	}
	if err := team.Validate(); err != nil {
		return Requirement{}, err
	}
	// Validation has already refused every character that could close a quote
	// or introduce an operator, so this interpolation cannot produce an
	// expression other than the intended one.
	return Requirement{expression: fmt.Sprintf(
		`identifier "%s" and anchor apple generic `+
			`and certificate 1[field.%s] `+
			`and certificate leaf[field.%s] `+
			`and certificate leaf[subject.OU] = "%s"`,
		string(identifier),
		developerIDIntermediateOID,
		developerIDLeafOID,
		string(team),
	)}, nil
}

// Expression is the text handed to the platform. It exists for the one call
// that needs it and for tests that assert the generated shape.
func (requirement Requirement) Expression() string {
	return requirement.expression
}

// Usable reports whether this requirement was produced by
// DeveloperIDRequirement. A zero value cannot be, which is what makes the type
// rather than a string check the guarantee.
func (requirement Requirement) Usable() bool {
	return requirement.expression != ""
}
