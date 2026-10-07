// Package portal implements the LilyPad Portal (email-based web registration /
// login that mints a durable platform XUID) and the Gree GameLib platform shim
// the patched client talks to. Identity is anchored on the Portal credential
// record so accounts survive client reinstalls.
package portal

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters (OWASP-ish defaults; tune via deployment if needed).
const (
	argonTime    = 1
	argonMemory  = 64 * 1024 // 64 MiB
	argonThreads = 4
	argonKeyLen  = 32
	argonSaltLen = 16
)

// Limit aggregate Argon2 memory across simultaneous public authentication calls.
var passwordWorkers = make(chan struct{}, 2)

// HashPassword returns a PHC-format argon2id hash string for pw.
func HashPassword(pw string) (string, error) {
	if len(pw) > 1024 {
		return "", fmt.Errorf("password too long")
	}
	passwordWorkers <- struct{}{}
	defer func() { <-passwordWorkers }()
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword reports whether pw matches the encoded argon2id hash.
func VerifyPassword(encoded, pw string) (bool, error) {
	if len(pw) > 1024 {
		return false, fmt.Errorf("password too long")
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, fmt.Errorf("portal: bad hash format")
	}
	var version, mem, time, threads int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, err
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &time, &threads); err != nil {
		return false, err
	}
	if version != argon2.Version || mem <= 0 || mem > argonMemory || time <= 0 || time > 3 || threads <= 0 || threads > argonThreads {
		return false, fmt.Errorf("invalid password hash parameters")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, err
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, err
	}
	if len(want) != argonKeyLen || len(salt) < 8 || len(salt) > 64 {
		return false, fmt.Errorf("invalid password hash length")
	}
	passwordWorkers <- struct{}{}
	defer func() { <-passwordWorkers }()
	got := argon2.IDKey([]byte(pw), salt, uint32(time), uint32(mem), uint8(threads), uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// randHex returns n random bytes as lowercase hex (2n chars).
func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// GenerateXUID mints a durable 32-hex platform identity.
func GenerateXUID() string { return randHex(16) }

// migrationAlphabet excludes ambiguous characters (0/O, 1/I) for legibility.
const migrationAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func randFromAlphabet(n int, alphabet string) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	out := make([]byte, n)
	for i := range b {
		out[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(out)
}

// GenerateMigrationCode mints a 16-char 引き継ぎ code.
func GenerateMigrationCode() string { return randFromAlphabet(16, migrationAlphabet) }

// GenerateMigrationPassword mints a 16-char 引き継ぎ password.
func GenerateMigrationPassword() string {
	const alpha = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789"
	return randFromAlphabet(16, alpha)
}

// GenerateToken mints a URL-safe opaque token (verification / session).
func GenerateToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Migration passwords use ROT13(reverse(base64(UTF8(password)))) on the wire.
// This reversible transport is decoded before verifying the stored Argon2 hash.

// DecodeMigrationPassword inverts the client transport (un-ROT13 -> un-reverse ->
// base64-decode) to recover the plaintext migration password.
func DecodeMigrationPassword(sent string) (string, error) {
	b64 := reverseString(rot13(strings.TrimSpace(sent)))
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		// Tolerate a missing-padding variant.
		raw, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(b64, "="))
		if err != nil {
			return "", fmt.Errorf("portal: bad migration password encoding: %w", err)
		}
	}
	return string(raw), nil
}

// VerifyMigrationPassword reports whether the client-sent migration password
// decodes to a plaintext matching the stored argon2 hash. It returns the decoded
// plaintext for diagnostics on mismatch.
func VerifyMigrationPassword(storedHash, sent string) (ok bool, decoded string) {
	pw, err := DecodeMigrationPassword(sent)
	if err != nil {
		return false, ""
	}
	match, _ := VerifyPassword(storedHash, pw)
	return match, pw
}

// rot13 applies ROT13 to ASCII letters (it is its own inverse), matching the
// game's native sub_180070720 transform.
func rot13(s string) string {
	b := []byte(s)
	for i := range b {
		switch c := b[i]; {
		case c >= 'A' && c <= 'Z':
			b[i] = 'A' + (c-'A'+13)%26
		case c >= 'a' && c <= 'z':
			b[i] = 'a' + (c-'a'+13)%26
		}
	}
	return string(b)
}

func reverseString(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}
