package hostsecret

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// One factory owns its operation seam. Every Open certifies the exact ancestor
// chain again, including a new factory after a failed Open left existing but
// unsynced directory entries. The filesystem/volume root is the stable anchor;
// existence of any other component is never accepted as durability evidence.
type secretDirectoryProvisioner struct {
	mu       sync.Mutex
	boundary string
	leaf     string
	ops      secretDirectoryOps
}

type secretDirectoryOps struct {
	mkdir         func(string, os.FileMode) error
	chmod         func(string, os.FileMode) error
	syncDirectory func(string) error
}

func newSecretDirectoryProvisioner(boundary, leaf string) *secretDirectoryProvisioner {
	return &secretDirectoryProvisioner{
		boundary: boundary,
		leaf:     leaf,
		ops: secretDirectoryOps{
			mkdir:         os.Mkdir,
			chmod:         os.Chmod,
			syncDirectory: syncSecretDirectory,
		},
	}
}

func (provisioner *secretDirectoryProvisioner) prepare(ctx context.Context) error {
	provisioner.mu.Lock()
	defer provisioner.mu.Unlock()
	failure := func() error { return &secretCommitError{phase: "directory provisioning"} }
	directories := secretDirectoryChain(provisioner.leaf)
	// Create from the stable root toward the credential boundary. Only newly
	// created components and the two owned boundaries receive permission changes.
	for index := len(directories) - 1; index >= 0; index-- {
		if ctx.Err() != nil {
			return failure()
		}
		path := directories[index]
		info, err := os.Lstat(path)
		created := false
		if errors.Is(err, os.ErrNotExist) {
			if err := provisioner.ops.mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return failure()
			}
			created = true
			info, err = os.Lstat(path)
		}
		owned := path == provisioner.boundary || path == provisioner.leaf
		if err != nil || (owned && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir())) {
			return failure()
		}
		// Existing ancestor aliases remain compatible with the previous MkdirAll
		// behavior (e.g. macOS /var); the owned boundaries themselves reject aliases.
		if !info.IsDir() {
			if followed, err := os.Stat(path); err != nil || !followed.IsDir() {
				return failure()
			}
		}
		if created || owned {
			if err := provisioner.ops.chmod(path, 0o700); err != nil {
				return failure()
			}
		}
	}
	// An existing ancestor alias (such as macOS /var) can name a different
	// physical ancestor chain. Certify that exact target chain as well; syncing
	// the alias's logical parents alone would leave target entries unproved.
	physicalLeaf, err := filepath.EvalSymlinks(provisioner.leaf)
	if err != nil {
		return failure()
	}
	if physicalLeaf != provisioner.leaf {
		directories = append(directories, secretDirectoryChain(physicalLeaf)...)
	}
	// Keep intermediate alias targets too: /a -> /other/b -> /physical depends
	// on the entry for b in /other, even though EvalSymlinks returns /physical.
	seen := make(map[string]bool, len(directories))
	for _, path := range directories {
		seen[path] = true
	}
	for index := 0; index < len(directories); index++ {
		path := directories[index]
		info, err := os.Lstat(path)
		if err != nil {
			return failure()
		}
		if info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		target, err := os.Readlink(path)
		if err != nil || target != filepath.Clean(target) {
			return failure()
		}
		if !filepath.IsAbs(target) {
			parent, err := filepath.EvalSymlinks(filepath.Dir(path))
			if err != nil {
				return failure()
			}
			target = filepath.Join(parent, target)
		}
		for _, targetPath := range secretDirectoryChain(target) {
			if !seen[targetPath] {
				seen[targetPath] = true
				directories = append(directories, targetPath)
			}
		}
	}
	// Bottom-up barriers persist each directory's metadata and the child entry
	// in its parent. Syncing the root closes the exact path, without syncing
	// unrelated directories or opening any unrelated files.
	for _, path := range directories {
		if ctx.Err() != nil {
			return failure()
		}
		if err := provisioner.ops.syncDirectory(path); err != nil {
			return failure()
		}
	}
	if ctx.Err() != nil {
		return failure()
	}
	return nil
}

func secretDirectoryChain(leaf string) []string {
	var directories []string
	for path := leaf; ; path = filepath.Dir(path) {
		directories = append(directories, path)
		if filepath.Dir(path) == path {
			return directories
		}
	}
}
