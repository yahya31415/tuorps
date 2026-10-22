package connhttp

import (
	"testing"

	"github.com/yahya31415/tuorps/internal/platform/httpx/httpxtest"
)

func FuzzAuthorizeRequest(f *testing.F) {
	httpxtest.FuzzDecode[authorizeRequest](f, `{"provider":"meta"}`, `{"provider":"x"}`)
}

func FuzzConnectRequest(f *testing.F) {
	httpxtest.FuzzDecode[connectRequest](f, `{"grant_id":"0199c3f0-0000-7000-8000-000000004a01","external_ids":["p1","ig1"]}`, `{"grant_id":"x","external_ids":[]}`)
}
