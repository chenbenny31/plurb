// Package auth hashes passwords and talks to external login providers.
package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// PBKDF2-HMAC-SHA256 at OWASP's 2023 recommended work factor. The iteration
// count is stored in each hash, so it can be raised later without breaking
// existing passwords.
const (
	iterations = 600_000
	saltLen    = 16
	keyLen     = 32
	scheme     = "pbkdf2-sha256"
	MinLength  = 10
	MaxLength  = 256
)

var ErrWeakPassword = fmt.Errorf("password must be %d–%d characters", MinLength, MaxLength)

// Hash returns "pbkdf2-sha256$<iterations>$<salt>$<key>" (base64, no padding).
func Hash(password string) (string, error) {
	if n := len([]rune(password)); n < MinLength || len(password) > MaxLength {
		return "", ErrWeakPassword
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, iterations, keyLen)
	if err != nil {
		return "", err
	}
	enc := base64.RawStdEncoding
	return fmt.Sprintf("%s$%d$%s$%s", scheme, iterations, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// Verify reports whether password matches hash. An empty or malformed hash
// never matches (OAuth-only accounts have no password).
func Verify(hash, password string) bool {
	ok, _ := verify(hash, password)
	return ok
}

func verify(hash, password string) (bool, error) {
	parts := strings.Split(hash, "$")
	if len(parts) != 4 || parts[0] != scheme {
		return false, errors.New("unknown hash format")
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1 {
		return false, errors.New("bad iteration count")
	}
	enc := base64.RawStdEncoding
	salt, err1 := enc.DecodeString(parts[2])
	want, err2 := enc.DecodeString(parts[3])
	if err1 != nil || err2 != nil || len(password) > MaxLength {
		return false, errors.New("bad encoding")
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iter, len(want))
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// dummyHash lets callers spend the same time on unknown usernames as on
// real ones, so login timing doesn't reveal which usernames exist.
var dummyHash, _ = Hash("timing-equaliser-password")

// VerifyDummy burns one hash verification's worth of CPU.
func VerifyDummy(password string) { Verify(dummyHash, password) }
