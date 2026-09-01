// Package password hashes and verifies passwords with Argon2id using the pure
// Go implementation (golang.org/x/crypto/argon2), so builds do not depend on a
// system libargon2 and work on Linux, macOS and Windows. Hashes use the PHC
// string format ($argon2id$v=19$m=...,t=...,p=...$salt$hash), which is also the
// format the previous cgo libargon2 binding produced, so existing stored hashes
// continue to verify without rehashing.
package password

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 1
	argonKeyLen  = 32
	saltLen      = 16
)

// Hash derives a PHC-encoded Argon2id hash for the password.
func Hash(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		19, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// Verify checks a password against a PHC-encoded Argon2id hash using a
// constant-time comparison. The decoder tolerates both padded and unpadded
// base64 so hashes produced by different argon2 libraries verify correctly.
func Verify(encoded, password string) bool {
	salt, key, time, memory, threads, err := parsePHC(encoded)
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, time, memory, threads, uint32(len(key)))
	return subtle.ConstantTimeCompare(got, key) == 1
}

func parsePHC(encoded string) (salt, key []byte, t, m uint32, p uint8, err error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 {
		return nil, nil, 0, 0, 0, errors.New("malformed password hash")
	}
	if parts[1] != "argon2id" {
		return nil, nil, 0, 0, 0, errors.New("unsupported hash algorithm")
	}
	if parts[2] != "v=19" {
		return nil, nil, 0, 0, 0, errors.New("unsupported hash version")
	}
	// m=65536,t=3,p=1
	params := strings.Split(parts[3], ",")
	if len(params) != 3 {
		return nil, nil, 0, 0, 0, errors.New("malformed hash parameters")
	}
	var mi, ti int
	if _, e := fmt.Sscanf(params[0], "m=%d", &mi); e != nil {
		return nil, nil, 0, 0, 0, errors.New("malformed memory parameter")
	}
	if _, e := fmt.Sscanf(params[1], "t=%d", &ti); e != nil {
		return nil, nil, 0, 0, 0, errors.New("malformed iterations parameter")
	}
	var pi int
	if _, e := fmt.Sscanf(params[2], "p=%d", &pi); e != nil {
		return nil, nil, 0, 0, 0, errors.New("malformed parallelism parameter")
	}
	if mi <= 0 || ti <= 0 || pi <= 0 {
		return nil, nil, 0, 0, 0, errors.New("invalid hash parameters")
	}
	salt, err = decodeB64(parts[4])
	if err != nil {
		return nil, nil, 0, 0, 0, errors.New("malformed salt")
	}
	key, err = decodeB64(parts[5])
	if err != nil || len(key) < 16 {
		return nil, nil, 0, 0, 0, errors.New("malformed hash")
	}
	return salt, key, uint32(ti), uint32(mi), uint8(pi), nil
}

// decodeB64 accepts standard base64 with or without padding.
func decodeB64(s string) ([]byte, error) {
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
}