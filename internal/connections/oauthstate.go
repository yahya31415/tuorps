package connections

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"time"
	"uuid"

	"github.com/redis/go-redis/v9"

	"github.com/yahya31415/tuorps/internal/connections/core"
	"github.com/yahya31415/tuorps/internal/platform/keyring"
)

// ErrStateNotFound covers unknown, expired, already used and malformed
// states alike.
var ErrStateNotFound = errors.New("connections: OAuth state not found")

const (
	statePrefix = "tuorps:v1:oauth:"
	stateAAD    = "oauth/state"
)

// StatePayload is what an OAuth flow carries from authorize to callback.
// The callback accepts it only for the same user, session and provider.
type StatePayload struct {
	UserID      uuid.UUID
	SessionHash [32]byte // SHA-256 of the session token that started the flow
	WorkspaceID uuid.UUID
	BrandID     uuid.UUID
	Provider    core.Provider
	ReconnectID uuid.UUID // the connection being reconnected, or nil
}

// StateStore keeps OAuth states in Redis: hashed key, sealed value, a TTL,
// and an atomic GETDEL so each state is used at most once (ADR 0008).
type StateStore struct {
	rdb  *redis.Client
	keys *keyring.Keyring
	rand io.Reader
	ttl  time.Duration
}

// NewStateStore returns a StateStore whose states live for ttl.
func NewStateStore(rdb *redis.Client, keys *keyring.Keyring, rand io.Reader, ttl time.Duration) StateStore {
	return StateStore{rdb: rdb, keys: keys, rand: rand, ttl: ttl}
}

func stateKey(state string) string {
	h := sha256.Sum256([]byte(state))
	return statePrefix + hex.EncodeToString(h[:])
}

// Create stores p and returns the state to send to the provider: 32 random
// bytes, base64url-encoded.
func (s StateStore) Create(ctx context.Context, p StatePayload) (string, error) {
	raw := make([]byte, 32)
	if _, err := io.ReadFull(s.rand, raw); err != nil {
		return "", fmt.Errorf("connections: state: %w", err)
	}
	state := base64.RawURLEncoding.EncodeToString(raw)
	body, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("connections: state payload: %w", err)
	}
	sealed, err := s.keys.Seal(s.rand, body, []byte(stateAAD))
	if err != nil {
		return "", fmt.Errorf("connections: seal state: %w", err)
	}
	ok, err := s.rdb.SetNX(ctx, stateKey(state), sealed.Encode(), s.ttl).Result()
	if err != nil {
		return "", fmt.Errorf("connections: store state: %w", err)
	}
	if !ok {
		return "", errors.New("connections: state collision")
	}
	return state, nil
}

// Consume returns the payload of state and deletes it, atomically.
func (s StateStore) Consume(ctx context.Context, state string) (StatePayload, error) {
	if state == "" {
		return StatePayload{}, ErrStateNotFound
	}
	val, err := s.rdb.GetDel(ctx, stateKey(state)).Result()
	if errors.Is(err, redis.Nil) {
		return StatePayload{}, ErrStateNotFound
	}
	if err != nil {
		return StatePayload{}, fmt.Errorf("connections: consume state: %w", err)
	}
	sealed, err := keyring.Decode(val)
	if err != nil {
		return StatePayload{}, ErrStateNotFound
	}
	body, err := s.keys.Open(sealed, []byte(stateAAD))
	if err != nil {
		return StatePayload{}, ErrStateNotFound
	}
	var p StatePayload
	if err := json.Unmarshal(body, &p); err != nil {
		return StatePayload{}, ErrStateNotFound
	}
	return p, nil
}
