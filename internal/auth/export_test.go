package auth

// SessionKeyForTest exposes the Redis key of a session token to tests.
func SessionKeyForTest(token string) string {
	k, _ := sessionKey(token)
	return k
}
