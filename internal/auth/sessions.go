package auth

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"uuid"

	"github.com/redis/go-redis/v9"

	"github.com/yahya31415/tuorps/internal/auth/core"
	"github.com/yahya31415/tuorps/internal/platform/clock"
)

const (
	sessionPrefix     = "tuorps:v1:sess:"
	userSessionPrefix = "tuorps:v1:usess:"
)

// SessionStore keeps sessions in Redis under the SHA-256 of their token, so
// a Redis dump cannot be replayed as cookies.
type SessionStore struct {
	rdb    *redis.Client
	policy core.SessionPolicy
	now    clock.Now
	rand   io.Reader
}

// NewSessionStore returns a SessionStore.
func NewSessionStore(rdb *redis.Client, policy core.SessionPolicy, now clock.Now, rand io.Reader) SessionStore {
	return SessionStore{rdb: rdb, policy: policy, now: now, rand: rand}
}

func sessionKey(token string) (string, bool) {
	h, ok := core.HashToken(token)
	if !ok {
		return "", false
	}
	return sessionPrefix + hex.EncodeToString(h[:]), true
}

func userKey(u uuid.UUID) string { return userSessionPrefix + u.String() }

// Create starts a session for user and returns its token (for the cookie).
func (s SessionStore) Create(ctx context.Context, user uuid.UUID, mfaVerified bool) (string, error) {
	raw := make([]byte, 32)
	if _, err := io.ReadFull(s.rand, raw); err != nil {
		return "", fmt.Errorf("auth: session token: %w", err)
	}
	token, err := core.EncodeToken(raw)
	if err != nil {
		return "", err
	}
	key, _ := sessionKey(token)
	sess := core.NewSession(user, s.now(), mfaVerified)
	v := core.EvaluateSession(sess, s.policy, s.now())
	body, err := json.Marshal(sess)
	if err != nil {
		return "", err
	}
	_, err = s.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.Set(ctx, key, body, v.TTL)
		p.SAdd(ctx, userKey(user), key)
		p.Expire(ctx, userKey(user), s.policy.Absolute)
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("auth: store session: %w", err)
	}
	return token, nil
}

// Get returns the live session for token. Expired sessions are deleted;
// active ones have LastSeen refreshed (at most once a minute).
func (s SessionStore) Get(ctx context.Context, token string) (core.Session, bool, error) {
	key, ok := sessionKey(token)
	if !ok {
		return core.Session{}, false, nil
	}
	body, err := s.rdb.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return core.Session{}, false, nil
	}
	if err != nil {
		return core.Session{}, false, fmt.Errorf("auth: load session: %w", err)
	}
	var sess core.Session
	if err := json.Unmarshal(body, &sess); err != nil {
		return core.Session{}, false, s.rdb.Del(ctx, key).Err()
	}
	v := core.EvaluateSession(sess, s.policy, s.now())
	if !v.Valid {
		return core.Session{}, false, s.deleteKey(ctx, sess.UserID, key)
	}
	if v.Refresh {
		body, err := json.Marshal(v.Session)
		if err != nil {
			return core.Session{}, false, err
		}
		if err := s.rdb.Set(ctx, key, body, v.TTL).Err(); err != nil {
			return core.Session{}, false, fmt.Errorf("auth: refresh session: %w", err)
		}
	}
	return v.Session, true, nil
}

// Delete ends the session for token (logout).
func (s SessionStore) Delete(ctx context.Context, token string) error {
	key, ok := sessionKey(token)
	if !ok {
		return nil
	}
	body, err := s.rdb.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil
	}
	if err != nil {
		return err
	}
	var sess core.Session
	_ = json.Unmarshal(body, &sess)
	return s.deleteKey(ctx, sess.UserID, key)
}

// DeleteAllForUser ends every session of user except keepToken ("" ends all),
// e.g. after a password change.
func (s SessionStore) DeleteAllForUser(ctx context.Context, user uuid.UUID, keepToken string) error {
	keep, _ := sessionKey(keepToken)
	keys, err := s.rdb.SMembers(ctx, userKey(user)).Result()
	if err != nil {
		return fmt.Errorf("auth: list sessions: %w", err)
	}
	_, err = s.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		for _, k := range keys {
			if k != keep {
				p.Del(ctx, k)
				p.SRem(ctx, userKey(user), k)
			}
		}
		return nil
	})
	return err
}

func (s SessionStore) deleteKey(ctx context.Context, user uuid.UUID, key string) error {
	_, err := s.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.Del(ctx, key)
		p.SRem(ctx, userKey(user), key)
		return nil
	})
	return err
}
