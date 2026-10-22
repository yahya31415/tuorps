package core

import "uuid"

// TokenField names which token a ciphertext holds.
type TokenField string

const (
	FieldAccessToken  TokenField = "access_token"
	FieldRefreshToken TokenField = "refresh_token"
	FieldPageToken    TokenField = "page_token"
)

// TokenAAD is the additional authenticated data that binds a sealed token
// to its grant or connection row and to its field (ADR 0009).
func TokenAAD(rowID uuid.UUID, f TokenField) []byte {
	return []byte("connections/" + rowID.String() + "/" + string(f))
}
