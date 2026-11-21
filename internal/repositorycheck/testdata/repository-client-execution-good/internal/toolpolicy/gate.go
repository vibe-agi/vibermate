package toolpolicy

import "path/filepath"

func clean(path string) string {
	return filepath.Clean(path)
}
