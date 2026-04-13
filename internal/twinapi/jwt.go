package twinapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// jwtHeader is the fixed header for all JWTs minted by the twin.
var jwtHeader = base64URLEncode(mustJSON(map[string]string{
	"alg": "HS256",
	"typ": "JWT",
}))

type jwtClaims struct {
	Sub   string `json:"sub"`
	Email string `json:"email"`
	Name  string `json:"name"`
	Tier  string `json:"tier"`
	Exp   int64  `json:"exp"`
	Iat   int64  `json:"iat"`
}

// mintJWT creates an HMAC-SHA256 signed JWT for the given user.
func (s *store) mintJWT(user *User, expiresIn time.Duration) string {
	now := s.now()
	claims := jwtClaims{
		Sub:   user.ID,
		Email: user.Email,
		Name:  user.Name,
		Tier:  user.Tier,
		Exp:   now.Add(expiresIn).Unix(),
		Iat:   now.Unix(),
	}

	payload := base64URLEncode(mustJSON(claims))
	signingInput := jwtHeader + "." + payload

	s.mu.RLock()
	secret := s.jwtSecret
	s.mu.RUnlock()

	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signingInput))
	sig := base64URLEncode(mac.Sum(nil))

	return signingInput + "." + sig
}

// validateJWT verifies signature and expiry, returning claims on success.
func (s *store) validateJWT(tokenStr string) (*jwtClaims, error) {
	parts := strings.SplitN(tokenStr, ".", 3)
	if len(parts) != 3 {
		return nil, errors.New("malformed JWT: expected 3 parts")
	}

	signingInput := parts[0] + "." + parts[1]

	s.mu.RLock()
	secret := s.jwtSecret
	s.mu.RUnlock()

	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signingInput))
	expectedSig := base64URLEncode(mac.Sum(nil))

	if !hmac.Equal([]byte(parts[2]), []byte(expectedSig)) {
		return nil, errors.New("invalid JWT signature")
	}

	claimsJSON, err := base64URLDecode(parts[1])
	if err != nil {
		return nil, fmt.Errorf("failed to decode JWT claims: %w", err)
	}

	var claims jwtClaims
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		return nil, fmt.Errorf("failed to parse JWT claims: %w", err)
	}

	now := s.now()
	if now.Unix() >= claims.Exp {
		return nil, errors.New("JWT expired")
	}

	return &claims, nil
}

func base64URLEncode(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}

func base64URLDecode(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("twinapi: failed to marshal JSON: %v", err))
	}
	return b
}
