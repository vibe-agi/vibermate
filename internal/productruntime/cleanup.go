package productruntime

import (
	"context"
	"errors"
	"fmt"
)

type cleanupFunc func(context.Context) error

type cleanupEntry struct {
	name string
	run  cleanupFunc
}

type cleanupStack struct {
	entries []cleanupEntry
}

func (s *cleanupStack) register(name string, cleanup cleanupFunc) {
	s.entries = append(s.entries, cleanupEntry{name: name, run: cleanup})
}

func (s *cleanupStack) shutdown(ctx context.Context) error {
	var result error
	for index := len(s.entries) - 1; index >= 0; index-- {
		entry := s.entries[index]
		if err := entry.run(ctx); err != nil {
			result = errors.Join(result, fmt.Errorf("shutdown %s: %w", entry.name, err))
		}
	}
	return result
}
