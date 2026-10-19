package httpx_test

import (
	"testing"

	"github.com/yahya31415/tuorps/internal/platform/httpx/httpxtest"
)

func httpxtestFuzz(f *testing.F) {
	httpxtest.FuzzDecode[fuzzBody](f, `{"name":"x"}`, `{"name":1}`)
}
