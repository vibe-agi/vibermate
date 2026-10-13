package hostsecret

import (
	"errors"
	"fmt"
	"os"

	"github.com/vibe-agi/vibermate/internal/secretstore"
)

// secretFileOps belongs to one store. Tests can wrap real file boundaries
// without replacing the public Store operations or sharing mutable hooks.
type secretFileOps struct {
	createTemp func(string, string) (secretCommitFile, error)
	rename     func(string, string) error
	syncParent func(string) error
}

type secretCommitFile interface {
	Name() string
	Chmod(os.FileMode) error
	Write([]byte) (int, error)
	Sync() error
	Close() error
}

func defaultSecretFileOps() secretFileOps {
	return secretFileOps{
		createTemp: func(directory, pattern string) (secretCommitFile, error) {
			return os.CreateTemp(directory, pattern)
		},
		rename:     replaceDevelopmentFile,
		syncParent: syncSecretParent,
	}
}

// A rename changes the authoritative file even if the following barrier fails.
// Keep this distinction internal; operating-system errors may contain secrets
// or paths and must never be included in the public diagnostic.
type secretCommitError struct {
	phase   string
	renamed bool
}

func (err *secretCommitError) Error() string {
	return fmt.Sprintf("%s: protected secret file commit failed (%s)", secretstore.ErrUnavailable, err.phase)
}

func (err *secretCommitError) Unwrap() error { return secretstore.ErrUnavailable }

// Called with store.mu held. Existing Values own independent copies, so clearing
// the cache does not revoke leases which were already issued.
func (store *developmentFileStore) quarantineUncertainCommit(err error) {
	var commitErr *secretCommitError
	if errors.As(err, &commitErr) && commitErr.renamed {
		store.unavailable = true
		destroyDevelopmentItems(store.items)
	}
}
