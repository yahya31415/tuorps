package authhttp

import (
	"testing"

	"github.com/yahya31415/tuorps/internal/platform/httpx/httpxtest"
)

func FuzzSignupRequest(f *testing.F) {
	httpxtest.FuzzDecode[signupRequest](f, `{"email":"a@example.in","password":"correct horse battery staple","name":"A","workspace_name":"W"}`, `{"email":1}`)
}

func FuzzLoginRequest(f *testing.F) {
	httpxtest.FuzzDecode[loginRequest](f, `{"email":"a@example.in","password":"x"}`)
}

func FuzzTokenRequest(f *testing.F) {
	httpxtest.FuzzDecode[tokenRequest](f, `{"token":"AAAA"}`)
}

func FuzzEmailRequest(f *testing.F) {
	httpxtest.FuzzDecode[emailRequest](f, `{"email":"a@example.in"}`)
}
