package core

import (
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	saltLen = 16
	keyLen  = 32

	// Bounds on parameters read back from storage, so a tampered hash cannot
	// make verification allocate gigabytes or spin for minutes.
	maxMemoryKiB = 1 << 20 // 1 GiB
	maxTime      = 10
	maxThreads   = 16
)

// ErrInvalidHash reports a password hash that is not a bounded argon2id PHC string.
var ErrInvalidHash = errors.New("auth: invalid password hash")

// Argon2Params are argon2id cost parameters.
type Argon2Params struct {
	Memory  uint32 // KiB
	Time    uint32
	Threads uint8
}

// DefaultArgon2 is the OWASP-recommended minimum: 46 MiB, 1 pass, 1 lane.
func DefaultArgon2() Argon2Params { return Argon2Params{Memory: 47104, Time: 1, Threads: 1} }

// HashPassword derives an argon2id hash and encodes it as a PHC string:
// $argon2id$v=19$m=<KiB>,t=<passes>,p=<lanes>$<salt>$<hash> (base64, no padding).
func HashPassword(pw string, salt []byte, p Argon2Params) (string, error) {
	if len(salt) < saltLen {
		return "", fmt.Errorf("%w: salt must be at least %d bytes", ErrInvalidHash, saltLen)
	}
	key := argon2.IDKey([]byte(pw), salt, p.Time, p.Memory, p.Threads, keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.Memory, p.Time, p.Threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword checks pw against a PHC hash in constant time. needsRehash
// reports that the hash was made with parameters other than current.
func VerifyPassword(pw, phc string, current Argon2Params) (ok, needsRehash bool, err error) {
	p, salt, want, err := parsePHC(phc)
	if err != nil {
		return false, false, err
	}
	got := argon2.IDKey([]byte(pw), salt, p.Time, p.Memory, p.Threads, keyLen)
	return subtle.ConstantTimeCompare(got, want) == 1, p != current, nil
}

func parsePHC(s string) (Argon2Params, []byte, []byte, error) {
	parts := strings.Split(s, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return Argon2Params{}, nil, nil, ErrInvalidHash
	}
	var p Argon2Params
	for _, kv := range strings.Split(parts[3], ",") {
		k, v, ok := strings.Cut(kv, "=")
		n, err := strconv.ParseUint(v, 10, 32)
		if !ok || err != nil {
			return Argon2Params{}, nil, nil, ErrInvalidHash
		}
		switch k {
		case "m":
			p.Memory = uint32(n)
		case "t":
			p.Time = uint32(n)
		case "p":
			if n > maxThreads {
				return Argon2Params{}, nil, nil, ErrInvalidHash
			}
			p.Threads = uint8(n)
		default:
			return Argon2Params{}, nil, nil, ErrInvalidHash
		}
	}
	if p.Memory < 8 || p.Memory > maxMemoryKiB || p.Time < 1 || p.Time > maxTime || p.Threads < 1 {
		return Argon2Params{}, nil, nil, ErrInvalidHash
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	key, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(salt) < 8 || len(salt) > 64 || len(key) != keyLen {
		return Argon2Params{}, nil, nil, ErrInvalidHash
	}
	return p, salt, key, nil
}
