package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters from ADR-003 D2 (OWASP minimum for argon2id: m=19 MiB, t=2, p=1).
const (
	argonMemoryKiB  = 19456
	argonIterations = 2
	argonThreads    = 1
	argonSaltLen    = 16
	argonKeyLen     = 32

	PINMinLength = 6
	PINMaxLength = 12
)

var (
	ErrPINFormat     = errors.New("PIN harus berupa 6-12 digit angka")
	ErrPINTooWeak    = errors.New("PIN terlalu mudah ditebak; hindari angka berulang atau berurutan")
	ErrPINHashFormat = errors.New("stored PIN hash is not a supported argon2id PHC string")
)

// weakPINs is a small denylist on top of the repeat/sequence rules (ADR-003 D2).
var weakPINs = map[string]bool{
	"121212": true, "112233": true, "123123": true, "102030": true,
	"111222": true, "202020": true, "696969": true, "147258": true,
}

// ValidatePINPolicy enforces the PIN policy at set/reset time. Login never applies it,
// so a policy change cannot lock out existing staff.
func ValidatePINPolicy(pin string) error {
	if len(pin) < PINMinLength || len(pin) > PINMaxLength {
		return ErrPINFormat
	}
	for _, r := range pin {
		if r < '0' || r > '9' {
			return ErrPINFormat
		}
	}
	if weakPINs[pin] || strings.Count(pin, pin[:1]) == len(pin) || isMonotonicRun(pin) || isBirthYearLike(pin) {
		return ErrPINTooWeak
	}
	return nil
}

func isMonotonicRun(pin string) bool {
	up, down := true, true
	for i := 1; i < len(pin); i++ {
		d := int(pin[i]) - int(pin[i-1])
		up = up && (d == 1 || d == -9)
		down = down && (d == -1 || d == 9)
	}
	return up || down
}

// isBirthYearLike rejects 6-digit PINs of the form 19xx19 / 20xx20 (year-shaped and easy to guess).
func isBirthYearLike(pin string) bool {
	return len(pin) == 6 && (pin[:2] == "19" || pin[:2] == "20") && pin[4:] == pin[:2]
}

// HashPIN derives an argon2id PHC string: $argon2id$v=19$m=19456,t=2,p=1$<salt>$<hash>.
func HashPIN(pin string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("failed to generate PIN salt: %w", err)
	}
	key := argon2.IDKey([]byte(pin), salt, argonIterations, argonMemoryKiB, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemoryKiB, argonIterations, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPIN checks a PIN against an argon2id PHC string in constant time. Parameters are read
// from the hash, so hashes created with older parameters keep verifying after a policy bump.
func VerifyPIN(pin, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, ErrPINHashFormat
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, ErrPINHashFormat
	}
	var memory, iterations uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil {
		return false, ErrPINHashFormat
	}
	// Bound attacker-influenced cost if a hash row were ever tampered with.
	if memory == 0 || memory > 256*1024 || iterations == 0 || iterations > 10 || threads == 0 {
		return false, ErrPINHashFormat
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return false, ErrPINHashFormat
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) < 16 {
		return false, ErrPINHashFormat
	}
	got := argon2.IDKey([]byte(pin), salt, iterations, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// dummyPINHash is verified against when the store/user does not exist or is inactive, so every
// login failure costs one argon2id computation and response timing does not reveal which part failed.
var dummyPINHash = func() string {
	h, err := HashPIN("000000-timing-equalizer")
	if err != nil {
		panic(err)
	}
	return h
}()

// BurnPINVerification spends the same work as a real verification and always fails.
func BurnPINVerification(pin string) {
	_, _ = VerifyPIN(pin, dummyPINHash)
}
