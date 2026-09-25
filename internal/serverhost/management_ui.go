package serverhost

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
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
		writer.Header().Set("X-Frame-Options", "DENY")
		writer.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		// Flutter's bootstrap, service worker, and main bundle keep stable names
		// across releases. Require revalidation for every member so loading a new
		// index can never execute a cached control client from an older contract.
		writer.Header().Set("Cache-Control", "no-cache")
		if clean == "/main.dart.js" {
			writer.Header().Add("Vary", "Accept-Encoding")
			if request.Header.Get("Range") == "" &&
				acceptsGzip(request.Header.Values("Accept-Encoding")) {
				compressed := &gzipResponseWriter{ResponseWriter: writer}
				files.ServeHTTP(compressed, request)
				if compressed.gzip != nil {
					_ = compressed.gzip.Close()
				}
				return
			}
		}
		files.ServeHTTP(writer, request)
	}), nil
}

func acceptsGzip(values []string) bool {
	for _, value := range values {
		for _, item := range strings.Split(value, ",") {
			encoding, parameters, err := mime.ParseMediaType(strings.TrimSpace(item))
			if err != nil || !strings.EqualFold(encoding, "gzip") {
				continue
			}
			if quality, specified := parameters["q"]; specified {
				value, err := strconv.ParseFloat(quality, 64)
				if err != nil || !(value > 0 && value <= 1) {
					continue
				}
			}
			return true
		}
	}
	return false
}

type gzipResponseWriter struct {
	http.ResponseWriter
	gzip   *gzip.Writer
	status int
}

func (writer *gzipResponseWriter) WriteHeader(status int) {
	writer.status = status
	if status == http.StatusOK {
		writer.Header().Del("Content-Length")
		writer.Header().Set("Content-Encoding", "gzip")
	}
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *gzipResponseWriter) Write(body []byte) (int, error) {
	if writer.status == 0 {
		writer.WriteHeader(http.StatusOK)
	}
	if writer.status != http.StatusOK {
		return writer.ResponseWriter.Write(body)
	}
	if writer.gzip == nil {
		writer.gzip = gzip.NewWriter(writer.ResponseWriter)
	}
	return writer.gzip.Write(body)
}
