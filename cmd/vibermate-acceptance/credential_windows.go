//go:build windows

package main

import (
	"errors"

	"github.com/vibe-agi/vibermate/internal/secretstore"
)

func readPrivateCredential(string) (*secretstore.Value, error) {
	return nil, errors.New("credentialed packaged acceptance requires macOS")
}
