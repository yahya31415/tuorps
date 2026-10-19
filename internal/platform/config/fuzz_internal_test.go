package config

import (
	"errors"
	"regexp"
	"slices"
	"testing"
)

// leafErrors flattens an errors.Join tree.
func leafErrors(err error) []error {
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		var out []error
		for _, e := range j.Unwrap() {
			out = append(out, leafErrors(e)...)
		}
		return out
	}
	return []error{err}
}

// FuzzLoadErrorsCarryNoInput checks structurally that Load cannot echo its
// input: every reported problem is a FieldError whose key is a variable name
// and whose cause is one of the package's fixed sentinel errors.
func FuzzLoadErrorsCarryNoInput(f *testing.F) {
	f.Add("20", "postgres://u:p@h/db", "1MiB", "info")
	f.Add("abc", "mysql://u:p@h/db", "zz", "loud")
	f.Add("-5", "", "0", "")
	keyRE := regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	f.Fuzz(func(t *testing.T, maxConns, dbURL, body, level string) {
		kv := map[string]string{
			"DATABASE_URL":        dbURL,
			"REDIS_URL":           "redis://r:6379",
			"DB_MAX_CONNS":        maxConns,
			"HTTP_MAX_BODY_BYTES": body,
			"LOG_LEVEL":           level,
		}
		_, err := Load(func(k string) (string, bool) { v, ok := kv[k]; return v, ok }, nil)
		if err == nil {
			return
		}
		for _, leaf := range leafErrors(err) {
			var fe *FieldError
			if !errors.As(leaf, &fe) || leaf != error(fe) {
				t.Fatalf("leaf %T is not a *FieldError: %v", leaf, leaf)
			}
			if !keyRE.MatchString(fe.Key) {
				t.Fatalf("key %q is not a variable name", fe.Key)
			}
			if !slices.Contains(sentinelErrors(), fe.Err) {
				t.Fatalf("cause %q of %s is not a fixed sentinel", fe.Err, fe.Key)
			}
		}
	})
}
