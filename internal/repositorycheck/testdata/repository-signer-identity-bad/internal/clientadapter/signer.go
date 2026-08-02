package clientadapter

// A hand-written requirement in the catalog: exactly what the rule forbids.
type Signer struct {
	Requirement string
}

func builtIn() Signer {
	return Signer{
		Requirement: `anchor apple generic and certificate leaf[subject.OU] = "ABCDE12345"`,
	}
}

// Reaching the platform tool outside internal/codesignature.
const verifier = "/usr/bin/codesign"

// Each fragment of the requirement language appears alone somewhere, so
// removing any one of them from the rule leaves a hole this fixture finds.
const (
	anchorOnly     = `anchor apple generic`
	leafClauseOnly = `certificate leaf[field.1.2.840.113635.100.6.1.13]`
	oidOnly        = `1.2.840.113635.100.6.2.6`
)
