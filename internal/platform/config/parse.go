package config

import (
	"errors"
	"log/slog"
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// MaxSize is the largest byte size ParseSize accepts (1 TiB).
const MaxSize int64 = 1 << 40

// Sentinel causes. FieldError only ever wraps one of these, so error text
// is fixed and cannot carry a variable's value.
var (
	ErrInvalid  = errors.New("invalid value")
	ErrConflict = errors.New("set both directly and via _FILE")

	errDuration = errors.New("invalid value: want a positive duration such as 15s")
	errPositive = errors.New("invalid value: want a positive integer")
	errNonNeg   = errors.New("invalid value: want a non-negative integer")
	errSize     = errors.New("invalid value: want a positive size such as 1MiB or 4096")
	errAddr     = errors.New("invalid value: want host:port")
	errEnv      = errors.New("invalid value: want dev or prod")
	errLevel    = errors.New("invalid value: want debug, info, warn or error")
	errScheme   = errors.New("invalid value: unsupported URL scheme")
	errURL      = errors.New("invalid value: not a URL")
	errFile     = errors.New("invalid value: cannot read secret file")
	errMinMax   = errors.New("invalid value: exceeds DB_MAX_CONNS")
)

// sentinelErrors lists every cause a FieldError may carry.
func sentinelErrors() []error {
	return []error{
		ErrMissing, ErrInvalid, ErrConflict,
		errDuration, errPositive, errNonNeg, errSize, errAddr, errEnv,
		errLevel, errScheme, errURL, errFile, errMinMax,
		errOrigin, errHTTPSRequired, errRemoved, errIdleAbsolute, errCIDR, errKeyring, errActiveKey,
		errPort, errSMTPTLS, errSMTPPlain, errMailFrom, errSMTPUsername,
	}
}

// isInvalid reports whether a FieldError cause is an invalid-value sentinel,
// all of which match ErrInvalid under errors.Is.
func isInvalid(err error) bool {
	return err != ErrMissing && err != ErrConflict
}

// ParseSize parses a positive byte size: a plain integer or an integer with
// a KiB, MiB or GiB suffix. Results above MaxSize are rejected.
func ParseSize(s string) (int64, error) {
	mult := int64(1)
	for _, u := range []struct {
		suffix string
		mult   int64
	}{{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30}} {
		if strings.HasSuffix(s, u.suffix) {
			s, mult = strings.TrimSuffix(s, u.suffix), u.mult
			break
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 || n > MaxSize/mult {
		return 0, errSize
	}
	return n * mult, nil
}

func parseDuration(s string) (time.Duration, error) {
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, errDuration
	}
	return d, nil
}

func parseInt32(s string, min int64, sentinel error) (int32, error) {
	n, err := strconv.ParseInt(s, 10, 32)
	if err != nil || n < min || n > math.MaxInt32 {
		return 0, sentinel
	}
	return int32(n), nil
}

func parsePositiveInt32(s string) (int32, error)    { return parseInt32(s, 1, errPositive) }
func parseNonNegativeInt32(s string) (int32, error) { return parseInt32(s, 0, errNonNeg) }

func parsePositiveInt(s string) (int, error) {
	n, err := parsePositiveInt32(s)
	return int(n), err
}

func parseAddr(s string) (string, error) {
	if _, _, err := net.SplitHostPort(s); err != nil {
		return "", errAddr
	}
	return s, nil
}

func parseEnv(s string) (Env, error) {
	switch e := Env(s); e {
	case EnvDev, EnvProd:
		return e, nil
	}
	return "", errEnv
}

func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, errLevel
}

// urlWithScheme validates that a secret URL parses and uses one of schemes.
func urlWithScheme(schemes ...string) func(Secret) (Secret, error) {
	return func(s Secret) (Secret, error) {
		u, err := url.Parse(s.Reveal())
		if err != nil || u.Host == "" {
			return Secret{}, errURL
		}
		for _, sc := range schemes {
			if u.Scheme == sc {
				return s, nil
			}
		}
		return Secret{}, errScheme
	}
}
