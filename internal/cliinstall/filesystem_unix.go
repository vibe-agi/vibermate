//go:build darwin || linux

package cliinstall

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

type anchoredDirectory struct {
	path string
	file *os.File
}

func openAnchoredDirectory(path string, private bool) (*anchoredDirectory, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	if resolved != path {
		return nil, errors.New("directory path must not traverse symbolic links")
	}
	fd, err := unix.Open(
		path,
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("could not retain directory identity")
	}
	fail := func(root error) (*anchoredDirectory, error) {
		_ = file.Close()
		return nil, root
	}
	opened, err := file.Stat()
	if err != nil {
		return fail(err)
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return fail(err)
	}
	if !opened.IsDir() || pathInfo.Mode()&os.ModeSymlink != 0 ||
		!pathInfo.IsDir() || !os.SameFile(opened, pathInfo) {
		return fail(errors.New("path must identify one stable, real directory"))
	}
	if private {
		if opened.Mode().Perm()&0o077 != 0 {
			return fail(errors.New(
				"private installation-record directory is accessible by another user",
			))
		}
		owned, ownerErr := fileOwnedByCurrentUser(opened)
		if ownerErr != nil {
			return fail(ownerErr)
		}
		if !owned {
			return fail(errors.New(
				"private installation-record directory belongs to another user",
			))
		}
	}
	return &anchoredDirectory{path: path, file: file}, nil
}

func ensurePrivateDirectory(path string) (*anchoredDirectory, error) {
	directory, err := openAnchoredDirectory(path, true)
	if err == nil {
		return directory, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return nil, err
	}
	return openAnchoredDirectory(path, true)
}

func (directory *anchoredDirectory) ensureCurrent() error {
	if directory == nil || directory.file == nil {
		return errors.New("directory handle is closed")
	}
	opened, err := directory.file.Stat()
	if err != nil {
		return err
	}
	current, err := os.Lstat(directory.path)
	if err != nil {
		return err
	}
	if current.Mode()&os.ModeSymlink != 0 || !current.IsDir() ||
		!os.SameFile(opened, current) {
		return errors.New("directory path changed after it was opened")
	}
	return nil
}

func (directory *anchoredDirectory) metadata(name string) (entryMetadata, error) {
	if err := directory.ensureCurrent(); err != nil {
		return entryMetadata{}, err
	}
	if err := validateEntryName(name); err != nil {
		return entryMetadata{}, err
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(
		int(directory.file.Fd()),
		name,
		&stat,
		unix.AT_SYMLINK_NOFOLLOW,
	); err != nil {
		return entryMetadata{}, directory.pathError("lstat", name, err)
	}
	mode := uint32(stat.Mode)
	kind := entryOther
	switch mode & unix.S_IFMT {
	case unix.S_IFREG:
		kind = entryRegular
	case unix.S_IFDIR:
		kind = entryDirectory
	case unix.S_IFLNK:
		kind = entrySymlink
	}
	return entryMetadata{
		identity:    formatFileIdentity(uint64(stat.Dev), stat.Ino),
		kind:        kind,
		size:        stat.Size,
		permissions: os.FileMode(mode & 0o777),
		links:       uint64(stat.Nlink),
	}, nil
}

func (directory *anchoredDirectory) openFile(
	name string,
	flags int,
	permissions os.FileMode,
) (*os.File, error) {
	if err := directory.ensureCurrent(); err != nil {
		return nil, err
	}
	if err := validateEntryName(name); err != nil {
		return nil, err
	}
	fd, err := unix.Openat(
		int(directory.file.Fd()),
		name,
		flags|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		uint32(permissions.Perm()),
	)
	if err != nil {
		return nil, directory.pathError("open", name, err)
	}
	file := os.NewFile(uintptr(fd), filepath.Join(directory.path, name))
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("could not retain opened-file identity")
	}
	return file, nil
}

func (directory *anchoredDirectory) readlink(name string) (string, error) {
	if err := directory.ensureCurrent(); err != nil {
		return "", err
	}
	if err := validateEntryName(name); err != nil {
		return "", err
	}
	buffer := make([]byte, maxPathBytes+1)
	count, err := unix.Readlinkat(int(directory.file.Fd()), name, buffer)
	if err != nil {
		return "", directory.pathError("readlink", name, err)
	}
	if count < 1 || count > maxPathBytes {
		return "", errors.New("terminal-command destination is outside the path bound")
	}
	return string(buffer[:count]), nil
}

func (directory *anchoredDirectory) symlink(destination, name string) error {
	if err := directory.ensureCurrent(); err != nil {
		return err
	}
	if err := validateEntryName(name); err != nil {
		return err
	}
	if err := unix.Symlinkat(destination, int(directory.file.Fd()), name); err != nil {
		return directory.pathError("symlink", name, err)
	}
	return nil
}

func (directory *anchoredDirectory) renameNoReplace(oldName, newName string) error {
	if err := directory.ensureCurrent(); err != nil {
		return err
	}
	if err := validateEntryName(oldName); err != nil {
		return err
	}
	if err := validateEntryName(newName); err != nil {
		return err
	}
	if err := renameNoReplaceAt(
		int(directory.file.Fd()),
		oldName,
		newName,
	); err != nil {
		return directory.pathError("rename without replacement", newName, err)
	}
	return nil
}

func (directory *anchoredDirectory) exchange(leftName, rightName string) error {
	if err := directory.ensureCurrent(); err != nil {
		return err
	}
	if err := validateEntryName(leftName); err != nil {
		return err
	}
	if err := validateEntryName(rightName); err != nil {
		return err
	}
	if err := exchangeAt(
		int(directory.file.Fd()),
		leftName,
		rightName,
	); err != nil {
		return directory.pathError("exchange", rightName, err)
	}
	return nil
}

func (directory *anchoredDirectory) unlink(name string) error {
	if err := directory.ensureCurrent(); err != nil {
		return err
	}
	if err := validateEntryName(name); err != nil {
		return err
	}
	if err := unix.Unlinkat(int(directory.file.Fd()), name, 0); err != nil {
		return directory.pathError("remove", name, err)
	}
	return nil
}

func (directory *anchoredDirectory) unlinkIfExists(name string) error {
	err := directory.unlink(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (directory *anchoredDirectory) sync() error {
	if err := directory.ensureCurrent(); err != nil {
		return err
	}
	return directory.file.Sync()
}

func (directory *anchoredDirectory) close() {
	if directory != nil && directory.file != nil {
		_ = directory.file.Close()
		directory.file = nil
	}
}

func (directory *anchoredDirectory) pathError(
	operation string,
	name string,
	err error,
) error {
	return &os.PathError{
		Op:   operation,
		Path: filepath.Join(directory.path, name),
		Err:  err,
	}
}

func validateEntryName(name string) error {
	if name == "" || name == "." || name == ".." ||
		filepath.Base(name) != name || strings.IndexByte(name, 0) >= 0 {
		return errors.New("filesystem entry name must be one bounded path component")
	}
	return nil
}

func fileIdentity(info os.FileInfo) (string, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return "", errors.New("filesystem does not expose a stable file identity")
	}
	return formatFileIdentity(uint64(stat.Dev), stat.Ino), nil
}

func formatFileIdentity(device, inode uint64) string {
	return fmt.Sprintf("%016x:%016x", device, inode)
}

func validFileIdentity(value string) bool {
	parts := strings.Split(value, ":")
	if len(parts) != 2 || len(parts[0]) != 16 || len(parts[1]) != 16 {
		return false
	}
	device, deviceErr := strconv.ParseUint(parts[0], 16, 64)
	inode, inodeErr := strconv.ParseUint(parts[1], 16, 64)
	return deviceErr == nil && inodeErr == nil && device != 0 && inode != 0 &&
		strings.ToLower(value) == value
}

func fileOwnedByCurrentUser(info os.FileInfo) (bool, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return false, errors.New("filesystem does not expose file ownership")
	}
	return stat.Uid == uint32(os.Geteuid()), nil
}

func fileLinkCount(info os.FileInfo) (uint64, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return 0, errors.New("filesystem does not expose file link count")
	}
	return uint64(stat.Nlink), nil
}
