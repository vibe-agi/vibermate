package hostsecret

import "errors"

var ErrUnsupportedPlatform = errors.New(
	"native SecretStore is unavailable on this build platform",
)
