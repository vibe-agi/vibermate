package hostsecret

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/vibe-agi/vibermate/internal/secretstore"
)

const (
	developmentFileSchema   = "vibermate.development-secret-store/v1"
	serverFileSchema        = "vibermate.server-secret-store/v1"
	maxDevelopmentFileBytes = 1 << 20
)

type developmentFileItem struct {
	Revision uint64 `json:"revision"`
	Value    string `json:"value"`
}

type developmentFileDocument struct {
	Schema string                         `json:"schema"`
	Items  map[string]developmentFileItem `json:"items"`
}

type developmentSecret struct {
	revision secretstore.Revision
	value    []byte
}

// DevelopmentFileFactory opens a build-tag-limited file Store. Values are
// plaintext-equivalent at rest and this factory must never enter a release
// sidecar.
type DevelopmentFileFactory struct {
	path string
}

var _ secretstore.Factory = DevelopmentFileFactory{}

func NewDevelopmentFileFactory(path string) (DevelopmentFileFactory, error) {
	if path == "" ||
		!filepath.IsAbs(path) ||
		filepath.Clean(path) != path ||
		filepath.Base(path) == "." ||
		filepath.Base(path) == string(filepath.Separator) {
		return DevelopmentFileFactory{},
			errors.New("development secret file path is invalid")
	}
	return DevelopmentFileFactory{path: path}, nil
}

func (factory DevelopmentFileFactory) Open(
	ctx context.Context,
) (secretstore.Store, error) {
	if ctx == nil {
		return nil, errors.New("development SecretStore factory context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if factory.path == "" {
		return nil, errors.New("development SecretStore factory is invalid")
	}
	store, err := openDevelopmentFileStore(
		ctx,
		factory.path,
		developmentFileSchema,
	)
	if err != nil {
		return nil, err
	}
	return store, nil
}

type developmentFileStore struct {
	mu     sync.Mutex
	path   string
	schema string
	items  map[string]developmentSecret
}

var _ secretstore.Store = (*developmentFileStore)(nil)

func openDevelopmentFileStore(
	ctx context.Context,
	path string,
	schema string,
) (*developmentFileStore, error) {
	if schema != developmentFileSchema && schema != serverFileSchema {
		return nil, errors.New("protected SecretStore schema is invalid")
	}
	if err := prepareDevelopmentDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	items, err := loadDevelopmentFile(ctx, path, schema)
	if err != nil {
		return nil, err
	}
	return &developmentFileStore{
		path:   path,
		schema: schema,
		items:  items,
	}, nil
}

func (store *developmentFileStore) Read(
	ctx context.Context,
	reference secretstore.Reference,
) (*secretstore.Value, error) {
	return store.read(ctx, reference, 0, false)
}

func (store *developmentFileStore) ReadAtRevision(
	ctx context.Context,
	reference secretstore.Reference,
	expected secretstore.Revision,
) (*secretstore.Value, error) {
	if expected == 0 || expected > secretstore.MaxRevision {
		return nil, secretstore.ErrRevisionConflict
	}
	return store.read(ctx, reference, expected, true)
}

func (store *developmentFileStore) read(
	ctx context.Context,
	reference secretstore.Reference,
	expected secretstore.Revision,
	pinned bool,
) (*secretstore.Value, error) {
	key, err := validateDevelopmentOperation(ctx, reference)
	if err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	item, found := store.items[key]
	if !found {
		return nil, secretstore.ErrNotFound
	}
	if pinned && item.revision != expected {
		return nil, secretstore.ErrRevisionConflict
	}
	value, err := secretstore.NewValue(item.value)
	if err != nil {
		return nil, fmt.Errorf(
			"%w: development secret state is invalid",
			secretstore.ErrUnavailable,
		)
	}
	return value, nil
}

func (store *developmentFileStore) Inspect(
	ctx context.Context,
	reference secretstore.Reference,
) (secretstore.Metadata, error) {
	key, err := validateDevelopmentOperation(ctx, reference)
	if err != nil {
		return secretstore.Metadata{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return secretstore.Metadata{}, err
	}
	item, found := store.items[key]
	if !found {
		return secretstore.Metadata{State: secretstore.StateMissing}, nil
	}
	return secretstore.Metadata{
		State:    secretstore.StateConfigured,
		Revision: item.revision,
	}, nil
}

func (store *developmentFileStore) Replace(
	ctx context.Context,
	command secretstore.ReplaceCommand,
) (secretstore.Metadata, error) {
	key, err := validateDevelopmentOperation(ctx, command.Reference)
	if err != nil {
		return secretstore.Metadata{}, err
	}
	if command.Value == nil {
		return secretstore.Metadata{}, errors.New(
			"development replacement secret value is nil",
		)
	}
	if command.ExpectedRevision > secretstore.MaxRevision {
		return secretstore.Metadata{}, secretstore.ErrRevisionConflict
	}
	value, err := command.Value.CopyBytes()
	if err != nil {
		return secretstore.Metadata{}, err
	}
	defer clear(value)

	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return secretstore.Metadata{}, err
	}
	current, found := store.items[key]
	var currentRevision secretstore.Revision
	if found {
		currentRevision = current.revision
	}
	if currentRevision != command.ExpectedRevision {
		return secretstore.Metadata{}, secretstore.ErrRevisionConflict
	}
	if currentRevision == secretstore.MaxRevision {
		return secretstore.Metadata{}, secretstore.ErrRevisionExhausted
	}
	revision := currentRevision + 1
	candidate := cloneDevelopmentItems(store.items)
	candidate[key] = developmentSecret{
		revision: revision,
		value:    bytes.Clone(value),
	}
	defer destroyDevelopmentItems(candidate)
	if err := persistDevelopmentFile(store.path, store.schema, candidate); err != nil {
		return secretstore.Metadata{}, err
	}

	if found {
		clear(current.value)
	}
	store.items[key] = developmentSecret{
		revision: revision,
		value:    bytes.Clone(value),
	}
	return secretstore.Metadata{
		State:    secretstore.StateConfigured,
		Revision: revision,
	}, nil
}

func (store *developmentFileStore) Delete(
	ctx context.Context,
	reference secretstore.Reference,
) error {
	key, err := validateDevelopmentOperation(ctx, reference)
	if err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	current, found := store.items[key]
	if !found {
		return secretstore.ErrNotFound
	}
	candidate := cloneDevelopmentItems(store.items)
	removed := candidate[key]
	clear(removed.value)
	delete(candidate, key)
	defer destroyDevelopmentItems(candidate)
	if err := persistDevelopmentFile(store.path, store.schema, candidate); err != nil {
		return err
	}
	clear(current.value)
	delete(store.items, key)
	return nil
}

func prepareDevelopmentDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf(
			"%w: development secret directory could not be created",
			secretstore.ErrUnavailable,
		)
	}
	info, err := os.Lstat(path)
	if err != nil ||
		!info.IsDir() ||
		info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf(
			"%w: development secret directory is invalid",
			secretstore.ErrUnavailable,
		)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf(
			"%w: development secret directory permissions could not be applied",
			secretstore.ErrUnavailable,
		)
	}
	return nil
}

func loadDevelopmentFile(
	ctx context.Context,
	path string,
	expectedSchema string,
) (map[string]developmentSecret, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return make(map[string]developmentSecret), nil
	}
	if err != nil ||
		!info.Mode().IsRegular() ||
		info.Mode()&os.ModeSymlink != 0 ||
		info.Size() <= 0 ||
		info.Size() > maxDevelopmentFileBytes ||
		(runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0) {
		return nil, invalidDevelopmentFile()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		return nil, invalidDevelopmentFile()
	}
	defer clear(encoded)
	var document developmentFileDocument
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return nil, invalidDevelopmentFile()
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) ||
		document.Schema != expectedSchema ||
		document.Items == nil {
		return nil, invalidDevelopmentFile()
	}
	canonical, err := json.Marshal(document)
	if err != nil {
		clear(canonical)
		return nil, invalidDevelopmentFile()
	}
	canonical = append(canonical, '\n')
	if !bytes.Equal(encoded, canonical) {
		clear(canonical)
		return nil, invalidDevelopmentFile()
	}
	clear(canonical)

	items := make(map[string]developmentSecret, len(document.Items))
	for rawReference, encodedItem := range document.Items {
		reference, err := secretstore.ParseReference(rawReference)
		if err != nil ||
			reference.String() != rawReference ||
			encodedItem.Revision == 0 ||
			secretstore.Revision(encodedItem.Revision) > secretstore.MaxRevision {
			destroyDevelopmentItems(items)
			return nil, invalidDevelopmentFile()
		}
		decoded, err := base64.StdEncoding.Strict().DecodeString(encodedItem.Value)
		if err != nil {
			destroyDevelopmentItems(items)
			return nil, invalidDevelopmentFile()
		}
		value, err := secretstore.NewValue(decoded)
		clear(decoded)
		if err != nil {
			destroyDevelopmentItems(items)
			return nil, invalidDevelopmentFile()
		}
		owned, err := value.CopyBytes()
		value.Destroy()
		if err != nil {
			destroyDevelopmentItems(items)
			return nil, invalidDevelopmentFile()
		}
		items[rawReference] = developmentSecret{
			revision: secretstore.Revision(encodedItem.Revision),
			value:    owned,
		}
	}
	return items, nil
}

func persistDevelopmentFile(
	path string,
	schema string,
	items map[string]developmentSecret,
) error {
	document := developmentFileDocument{
		Schema: schema,
		Items:  make(map[string]developmentFileItem, len(items)),
	}
	for reference, item := range items {
		document.Items[reference] = developmentFileItem{
			Revision: uint64(item.revision),
			Value:    base64.StdEncoding.EncodeToString(item.value),
		}
	}
	encoded, err := json.Marshal(document)
	if err != nil || len(encoded)+1 > maxDevelopmentFileBytes {
		clear(encoded)
		return fmt.Errorf(
			"%w: development secret state is too large",
			secretstore.ErrUnavailable,
		)
	}
	encoded = append(encoded, '\n')
	defer clear(encoded)

	file, err := os.CreateTemp(
		filepath.Dir(path),
		".vibermate-development-secrets-*",
	)
	if err != nil {
		return developmentWriteError()
	}
	temporaryPath := file.Name()
	committed := false
	defer func() {
		_ = file.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return developmentWriteError()
	}
	if _, err := file.Write(encoded); err != nil {
		return developmentWriteError()
	}
	if err := file.Sync(); err != nil {
		return developmentWriteError()
	}
	if err := file.Close(); err != nil {
		return developmentWriteError()
	}
	if err := replaceDevelopmentFile(temporaryPath, path); err != nil {
		return developmentWriteError()
	}
	committed = true
	return nil
}

// ServerFileFactory is the explicitly selected headless Server credential
// backend. Values are plaintext-equivalent at rest and protected by the
// Server data directory's owner-only filesystem boundary. It is available in
// native builds because a headless Linux Server cannot depend on a desktop
// login keychain.
type ServerFileFactory struct {
	dataDirectory string
	path          string
}

var _ secretstore.Factory = ServerFileFactory{}

func NewServerFileFactory(dataDirectory string) (ServerFileFactory, error) {
	if dataDirectory == "" ||
		!filepath.IsAbs(dataDirectory) ||
		filepath.Clean(dataDirectory) != dataDirectory ||
		filepath.Base(dataDirectory) == "." ||
		filepath.Base(dataDirectory) == string(filepath.Separator) {
		return ServerFileFactory{}, errors.New("Server data directory is invalid")
	}
	return ServerFileFactory{
		dataDirectory: dataDirectory,
		path: filepath.Join(
			dataDirectory,
			"server-secrets",
			"store.json",
		),
	}, nil
}

func (factory ServerFileFactory) Open(
	ctx context.Context,
) (secretstore.Store, error) {
	if ctx == nil {
		return nil, errors.New("Server SecretStore factory context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if factory.dataDirectory == "" || factory.path == "" {
		return nil, errors.New("Server SecretStore factory is invalid")
	}
	if err := prepareServerDataDirectory(factory.dataDirectory); err != nil {
		return nil, err
	}
	return openDevelopmentFileStore(ctx, factory.path, serverFileSchema)
}

func prepareServerDataDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf(
			"%w: Server data directory could not be created",
			secretstore.ErrUnavailable,
		)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf(
			"%w: Server data directory is invalid",
			secretstore.ErrUnavailable,
		)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(path, 0o700); err != nil {
			return fmt.Errorf(
				"%w: Server data directory permissions could not be applied",
				secretstore.ErrUnavailable,
			)
		}
	}
	return nil
}

func validateDevelopmentOperation(
	ctx context.Context,
	reference secretstore.Reference,
) (string, error) {
	if ctx == nil {
		return "", errors.New("development SecretStore context is nil")
	}
	key := reference.String()
	if key == "" {
		return "", secretstore.ErrInvalidReference
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return key, nil
}

func cloneDevelopmentItems(
	items map[string]developmentSecret,
) map[string]developmentSecret {
	cloned := make(map[string]developmentSecret, len(items))
	for reference, item := range items {
		cloned[reference] = developmentSecret{
			revision: item.revision,
			value:    bytes.Clone(item.value),
		}
	}
	return cloned
}

func destroyDevelopmentItems(items map[string]developmentSecret) {
	for reference, item := range items {
		clear(item.value)
		delete(items, reference)
	}
}

func invalidDevelopmentFile() error {
	return fmt.Errorf(
		"%w: development secret file is invalid",
		secretstore.ErrUnavailable,
	)
}

func developmentWriteError() error {
	return fmt.Errorf(
		"%w: development secret file could not be replaced",
		secretstore.ErrUnavailable,
	)
}
