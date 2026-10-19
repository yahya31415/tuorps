package wshttp

import (
	"testing"

	"github.com/yahya31415/tuorps/internal/platform/httpx/httpxtest"
)

func FuzzNameRequest(f *testing.F) {
	httpxtest.FuzzDecode[nameRequest](f, `{"name":"Kerala Spices"}`)
}

func FuzzRoleRequest(f *testing.F) {
	httpxtest.FuzzDecode[roleRequest](f, `{"role":"admin"}`)
}

func FuzzInvitationRequest(f *testing.F) {
	httpxtest.FuzzDecode[invitationRequest](f, `{"email":"a@example.in","grants":[{"brand_id":"0199c3f0-0000-7000-8000-00000000ba01","role":"viewer"}]}`)
}

func FuzzAcceptRequest(f *testing.F) {
	httpxtest.FuzzDecode[acceptRequest](f, `{"token":"t"}`)
}
