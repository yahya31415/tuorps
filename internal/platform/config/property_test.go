package config_test

import (
	"strings"
	"testing"
	"testing/quick"

	"github.com/yahya31415/tuorps/internal/platform/config"
)

// Property: whichever required variables are missing, the error names
// exactly those, and an invalid secret's value never appears in the error.
func TestPropertyMissingVarsAreNamedAndSecretsNeverEchoed(t *testing.T) {
	prop := func(dropDB, dropRedis bool, junk string) bool {
		kv := minimal()
		if dropDB {
			delete(kv, "DATABASE_URL")
		}
		secretish := "s3cr3t" + junk
		if dropRedis {
			delete(kv, "REDIS_URL")
		} else {
			kv["REDIS_URL"] = "ftp://user:" + secretish + "@h" // invalid scheme
		}

		_, err := config.Load(env(kv), files(nil))
		if err == nil {
			return false
		}
		msg := err.Error()
		return strings.Contains(msg, "DATABASE_URL") == dropDB &&
			strings.Contains(msg, "REDIS_URL") && // missing or invalid, it is always named
			!strings.Contains(msg, secretish)
	}
	if err := quick.Check(prop, nil); err != nil {
		t.Error(err)
	}
}

func FuzzParseSize(f *testing.F) {
	for _, s := range []string{"1", "4096", "1KiB", "512KiB", "2MiB", "1GiB", "0", "-1", "", "KiB", "1.5MiB", "99999999999999999999", "9TiB"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		n, err := config.ParseSize(s)
		if err != nil {
			return
		}
		if n <= 0 || n > config.MaxSize {
			t.Fatalf("ParseSize(%q) = %d, outside (0, %d]", s, n, config.MaxSize)
		}
	})
}
