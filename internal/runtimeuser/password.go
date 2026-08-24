package runtimeuser

import (
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"io"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	passwordMemoryKiB = 64 * 1024
	passwordPasses    = 3
	passwordThreads   = 1
	passwordSaltBytes = 16
	passwordKeyBytes  = 32
)

func hashPassword(password []byte, random io.Reader) (string, error) {
	salt := make([]byte, passwordSaltBytes)
	if _, err := io.ReadFull(random, salt); err != nil {
		return "", fmt.Errorf("generate Runtime User password salt: %w", err)
	}
	key := argon2.IDKey(
		password,
		salt,
		passwordPasses,
		passwordMemoryKiB,
		passwordThreads,
		passwordKeyBytes,
	)
	encoded := fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		passwordMemoryKiB,
		passwordPasses,
		passwordThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	)
	clear(key)
	return encoded, nil
}

func verifyPassword(password []byte, encoded string) bool {
	salt, expected, ok := decodePasswordHash(encoded)
	if !ok {
		return false
	}
	actual := argon2.IDKey(
		password,
		salt,
		passwordPasses,
		passwordMemoryKiB,
		passwordThreads,
		passwordKeyBytes,
	)
	matched := subtle.ConstantTimeCompare(actual, expected) == 1
	clear(actual)
	clear(expected)
	return matched
}

func validPasswordHash(encoded string) bool {
	_, expected, ok := decodePasswordHash(encoded)
	clear(expected)
	return ok
}

func decodePasswordHash(encoded string) ([]byte, []byte, bool) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" ||
		parts[2] != "v="+strconv.Itoa(argon2.Version) ||
		parts[3] != fmt.Sprintf(
			"m=%d,t=%d,p=%d",
			passwordMemoryKiB,
			passwordPasses,
			passwordThreads,
		) {
		return nil, nil, false
	}
	salt, saltErr := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	key, keyErr := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if saltErr != nil || keyErr != nil || len(salt) != passwordSaltBytes ||
		len(key) != passwordKeyBytes {
		clear(key)
		return nil, nil, false
	}
	return salt, key, true
}

func consumeInvalidLoginCost(password []byte) {
	key := argon2.IDKey(
		password,
		[]byte("vibermate-login!"),
		passwordPasses,
		passwordMemoryKiB,
		passwordThreads,
		passwordKeyBytes,
	)
	clear(key)
}
