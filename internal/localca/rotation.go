package localca

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/vibe-agi/vibermate/internal/certidentity"
)

const (
	rootResetRequestFile   = "root-reset-request.json"
	rootResetRequestSchema = "vibermate-local-root-reset-v1"
	maxRootResetRequest    = 4096
)

var ErrRootResetPending = errors.New("local Root reset is already pending")

type rootResetRequest struct {
	Schema   string       `json:"schema"`
	Revision RootRevision `json:"revision"`
	Digest   string       `json:"digest"`
}

func rootResetRequestPath(dataDirectory string) string {
	return filepath.Join(dataDirectory, rootResetRequestFile)
}

// RequestRootReset records one idempotent, identity-bound reset intent. It
// does not touch the Root files; a later daemon generation consumes the intent
// before opening localca.
func RequestRootReset(
	dataDirectory string,
	identity RootIdentity,
) error {
	if err := validateResetDataDirectory(dataDirectory); err != nil ||
		!identity.Valid() {
		return errors.Join(ErrRootResetFailed, err)
	}
	path := rootResetRequestPath(dataDirectory)
	request := rootResetRequest{
		Schema:   rootResetRequestSchema,
		Revision: identity.Revision(),
		Digest:   identity.Digest().String(),
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return errors.Join(ErrRootResetFailed, err)
	}
	if len(encoded) > maxRootResetRequest {
		return ErrRootResetFailed
	}
	if existing, readErr := readResetRequest(path); readErr == nil {
		if existing == request {
			return nil
		}
		return ErrRootResetPending
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return errors.Join(ErrRootResetFailed, readErr)
	}
	// Never expose the destination until the complete, synced intent exists.
	// In particular, two idempotent HTTP requests may reach this boundary at
	// the same time; publishing a directly-written O_EXCL file would let the
	// loser observe a transient empty/partial JSON document.
	file, err := os.CreateTemp(dataDirectory, ".root-reset-request-")
	if err != nil {
		return errors.Join(ErrRootResetFailed, err)
	}
	temporaryPath := file.Name()
	defer func() {
		_ = file.Close()
		_ = os.Remove(temporaryPath)
	}()
	if _, err := file.Write(encoded); err != nil {
		return errors.Join(ErrRootResetFailed, err)
	}
	if err := file.Sync(); err != nil {
		return errors.Join(ErrRootResetFailed, err)
	}
	if err := file.Close(); err != nil {
		return errors.Join(ErrRootResetFailed, err)
	}
	if err := os.Link(temporaryPath, path); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return errors.Join(ErrRootResetFailed, err)
		}
		existing, readErr := readResetRequest(path)
		if readErr != nil {
			return errors.Join(ErrRootResetFailed, readErr)
		}
		if existing != request {
			return ErrRootResetPending
		}
		return nil
	}
	if err := os.Remove(temporaryPath); err != nil {
		return errors.Join(ErrRootResetFailed, err)
	}
	temporaryPath = ""
	if err := syncDirectory(dataDirectory); err != nil {
		return errors.Join(ErrRootResetFailed, err)
	}
	return nil
}

// ApplyPendingRootReset consumes an identity-bound reset request before a new
// Authority is opened. It is safe to call repeatedly after a successful reset.
func ApplyPendingRootReset(ctx context.Context, dataDirectory string) error {
	if ctx == nil {
		return errors.Join(ErrRootResetFailed, errors.New("reset context is nil"))
	}
	if !validResetDataDirectoryPath(dataDirectory) {
		return errors.Join(ErrRootResetFailed, ErrInvalidOptions)
	}
	path := rootResetRequestPath(dataDirectory)
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return errors.Join(ErrRootResetFailed, err)
	}
	if err := validateResetDataDirectory(dataDirectory); err != nil {
		return errors.Join(ErrRootResetFailed, err)
	}
	request, err := readResetRequest(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.Join(ErrRootResetFailed, err)
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrRootResetFailed, context.Cause(ctx))
	}
	rootDirectory := filepath.Join(dataDirectory, "local-ca")
	if err := ensurePrivateDirectory(rootDirectory); err != nil {
		return errors.Join(ErrRootResetFailed, err)
	}
	paths := []string{
		filepath.Join(rootDirectory, rootKeyFile),
		filepath.Join(rootDirectory, rootCertFile),
		filepath.Join(rootDirectory, rootManifestFile),
	}
	present := make([]bool, len(paths))
	for index, candidate := range paths {
		info, statErr := os.Lstat(candidate)
		switch {
		case statErr == nil:
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
				info.Mode().Perm() != 0o600 {
				return errors.Join(ErrRootResetFailed, ErrRootStateInvalid)
			}
			present[index] = true
		case errors.Is(statErr, os.ErrNotExist):
		default:
			return errors.Join(ErrRootResetFailed, statErr)
		}
	}
	allPresent := present[0] && present[1] && present[2]
	if allPresent {
		revision, digest, identityErr := persistentRootIdentity(paths)
		if identityErr != nil {
			return errors.Join(ErrRootResetFailed, identityErr)
		}
		if revision == request.Revision+1 && digest.String() != request.Digest {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return errors.Join(ErrRootResetFailed, err)
			}
			return syncDirectory(dataDirectory)
		}
		if revision != request.Revision || digest.String() != request.Digest {
			return errors.Join(ErrRootResetFailed, ErrRootStateInvalid)
		}
	} else {
		// A reset can be interrupted while deleting the old generation or
		// while creating the replacement generation. The identity-bound marker
		// owns these three exact, non-symlink, 0600 paths, so every partial set
		// is disposable. Requiring a partial certificate to match the old
		// digest would strand the valid "new certificate written, manifest not
		// yet written" crash state forever.
	}
	for _, candidate := range paths {
		if err := os.Remove(candidate); err != nil && !errors.Is(err, os.ErrNotExist) {
			return errors.Join(ErrRootResetFailed, err)
		}
	}
	if err := syncDirectory(rootDirectory); err != nil {
		return errors.Join(ErrRootResetFailed, err)
	}
	return nil
}

func rootRevisionForCreate(rootDirectory string) (RootRevision, string, error) {
	path := rootResetRequestPath(filepath.Dir(rootDirectory))
	request, err := readResetRequest(path)
	if errors.Is(err, os.ErrNotExist) {
		return certidentity.InitialRootRevision, "", nil
	}
	if err != nil || request.Revision == ^RootRevision(0) {
		return 0, "", errors.Join(ErrRootResetFailed, err)
	}
	return request.Revision + 1, path, nil
}

func validateResetDataDirectory(path string) error {
	if !validResetDataDirectoryPath(path) {
		return ErrInvalidOptions
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() ||
		info.Mode().Perm() != 0o700 {
		return ErrInvalidOptions
	}
	return nil
}

func validResetDataDirectoryPath(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path &&
		path != filepath.VolumeName(path)+string(filepath.Separator)
}

func readResetRequest(path string) (rootResetRequest, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return rootResetRequest{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
		info.Mode().Perm() != 0o600 || info.Size() > maxRootResetRequest {
		return rootResetRequest{}, ErrRootResetFailed
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		return rootResetRequest{}, err
	}
	if len(encoded) > maxRootResetRequest {
		return rootResetRequest{}, ErrRootResetFailed
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var request rootResetRequest
	if err := decoder.Decode(&request); err != nil {
		return rootResetRequest{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return rootResetRequest{}, ErrRootResetFailed
	}
	digest, err := certidentity.ParseRootDigest(request.Digest)
	if request.Schema != rootResetRequestSchema || !request.Revision.Valid() ||
		err != nil || !digest.Valid() {
		return rootResetRequest{}, ErrRootResetFailed
	}
	return request, nil
}

func persistentRootIdentity(
	paths []string,
) (RootRevision, certidentity.RootDigest, error) {
	keyPEM, err := readBoundedFile(paths[0], maxCertificatePEM)
	if err != nil {
		return 0, certidentity.RootDigest{}, err
	}
	certPEM, err := readBoundedFile(paths[1], maxCertificatePEM)
	if err != nil {
		return 0, certidentity.RootDigest{}, err
	}
	manifestBytes, err := readBoundedFile(paths[2], maxCertificatePEM)
	if err != nil {
		return 0, certidentity.RootDigest{}, err
	}
	key, err := parsePrivateKey(keyPEM)
	if err != nil {
		return 0, certidentity.RootDigest{}, err
	}
	certificate, err := parseCertificate(certPEM)
	if err != nil {
		return 0, certidentity.RootDigest{}, err
	}
	if err := validateRootIdentityOnly(key, certificate); err != nil {
		return 0, certidentity.RootDigest{}, err
	}
	digest, err := certidentity.DigestRootCertificate(certificate.Raw)
	if err != nil {
		return 0, certidentity.RootDigest{}, ErrRootStateInvalid
	}
	revision, err := decodeRootManifest(manifestBytes, digest)
	if err != nil {
		return 0, certidentity.RootDigest{}, ErrRootStateInvalid
	}
	return revision, digest, nil
}

func validateRootIdentityOnly(key *ecdsa.PrivateKey, certificate *x509.Certificate) error {
	if key == nil || certificate == nil || !certificate.IsCA ||
		!certificate.BasicConstraintsValid || certificate.MaxPathLen != 0 ||
		!certificate.MaxPathLenZero || certificate.KeyUsage&x509.KeyUsageCertSign == 0 {
		return ErrRootStateInvalid
	}
	public, ok := certificate.PublicKey.(*ecdsa.PublicKey)
	if !ok || public.Curve != elliptic.P256() ||
		public.X.Cmp(key.PublicKey.X) != 0 || public.Y.Cmp(key.PublicKey.Y) != 0 {
		return ErrRootStateInvalid
	}
	if err := certificate.CheckSignatureFrom(certificate); err != nil {
		return fmt.Errorf("%w: Root is not self-signed", ErrRootStateInvalid)
	}
	return nil
}
