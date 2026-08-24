package serverhost

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func newManagementUI(root string) (http.Handler, error) {
	if root == "" {
		return nil, nil
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, errors.New("Runtime Server Web root must be an absolute clean directory")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, fmt.Errorf("open Runtime Server Web root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, errors.New("Runtime Server Web root is not a direct directory")
	}
	index, err := os.Lstat(filepath.Join(root, "index.html"))
	if err != nil || index.Mode()&os.ModeSymlink != 0 ||
		!index.Mode().IsRegular() {
		return nil, errors.New("Runtime Server Web root has no regular index.html")
	}
	if err := filepath.WalkDir(root, func(
		_ string,
		entry fs.DirEntry,
		walkErr error,
	) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("Runtime Server Web root contains a symbolic link")
		}
		if entry.IsDir() {
			return nil
		}
		member, err := entry.Info()
		if err != nil {
			return err
		}
		if !member.Mode().IsRegular() {
			return errors.New("Runtime Server Web root contains a special file")
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("inspect Runtime Server Web root: %w", err)
	}
	files := http.FileServer(http.Dir(root))
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request == nil || (request.Method != http.MethodGet && request.Method != http.MethodHead) {
			serverProblem(writer, http.StatusNotFound, "server_route_not_found")
			return
		}
		clean := path.Clean("/" + strings.TrimPrefix(request.URL.Path, "/"))
		if clean != request.URL.Path && !(request.URL.Path == "" && clean == "/") {
			serverProblem(writer, http.StatusNotFound, "server_route_not_found")
			return
		}
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		// Flutter's bootstrap, service worker, and main bundle keep stable names
		// across releases. Require revalidation for every member so loading a new
		// index can never execute a cached control client from an older contract.
		writer.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(writer, request)
	}), nil
}
