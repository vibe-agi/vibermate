package providertransport

import (
	"errors"

	"github.com/vibe-agi/vibermate/internal/egressprofile"
)

// A structural Resolved value is not authority to borrow a direct-only lease
// for another transport class. Check the actual target before admission/I/O.
func validateResolvedTarget(resolved egressprofile.Resolved, target Target) error {
	if err := resolved.Validate(); err != nil {
		return err
	}
	if err := target.validate(); err != nil {
		return err
	}
	if resolved.Source() == egressprofile.SelectionTransport && !isCleartextProviderTransport(target.TransportKind()) {
		return errors.New("transport selection requires a direct-only provider target")
	}
	return nil
}
