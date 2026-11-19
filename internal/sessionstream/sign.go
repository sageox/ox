package sessionstream

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// signatureVersion prefixes the canonical string so a future scheme can never
// verify against a v1 signature by accident.
const signatureVersion = "OX-SIG-V1"

// Signature verification errors. Callers map them to reasons, never echo detail.
var (
	ErrSignatureMissing   = errors.New("signature header missing")
	ErrSignatureMalformed = errors.New("signature header malformed")
	ErrSignatureExpired   = errors.New("signature outside the allowed clock window")
	ErrSignatureInvalid   = errors.New("signature does not verify")
)

// Signature is a parsed Ox-Signature header.
type Signature struct {
	KeyID string
	Unix  int64
	Sig   []byte
}

// KeyID derives a stable id from a public key: base64url (no padding) of the
// first 16 bytes of its SHA-256. Content-derived, so the server and the client
// agree without a lookup and a key can never be re-pointed at another id.
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return base64.RawURLEncoding.EncodeToString(sum[:16])
}

// EncodePublicKey renders a public key for RegisterKeyRequest.PublicKey.
func EncodePublicKey(pub ed25519.PublicKey) string {
	return base64.RawURLEncoding.EncodeToString(pub)
}

// DecodePublicKey parses RegisterKeyRequest.PublicKey.
func DecodePublicKey(s string) (ed25519.PublicKey, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("public key is not base64url: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("public key must be %d bytes, got %d", ed25519.PublicKeySize, len(raw))
	}
	return ed25519.PublicKey(raw), nil
}

// CanonicalString is exactly what a request signature covers. requestURI is the
// path plus the raw query as sent (r.URL.RequestURI on the server). The host is
// deliberately excluded so a proxy hop (web BFF to api-go) keeps signatures valid;
// replay to another conversation still fails because the path names it.
func CanonicalString(method, requestURI string, unix int64, body []byte) string {
	sum := sha256.Sum256(body)
	return signatureVersion + "\n" + strings.ToUpper(method) + "\n" + requestURI + "\n" +
		strconv.FormatInt(unix, 10) + "\n" + hex.EncodeToString(sum[:])
}

// Sign returns the Ox-Signature header value for one request.
func Sign(priv ed25519.PrivateKey, method, requestURI string, now time.Time, body []byte) string {
	unix := now.Unix()
	sig := ed25519.Sign(priv, []byte(CanonicalString(method, requestURI, unix, body)))
	pub, _ := priv.Public().(ed25519.PublicKey)
	return "v1; key=" + KeyID(pub) + "; t=" + strconv.FormatInt(unix, 10) +
		"; sig=" + base64.RawURLEncoding.EncodeToString(sig)
}

// ParseSignature parses an Ox-Signature header value.
func ParseSignature(header string) (Signature, error) {
	if strings.TrimSpace(header) == "" {
		return Signature{}, ErrSignatureMissing
	}
	parts := strings.Split(header, ";")
	if strings.TrimSpace(parts[0]) != "v1" {
		return Signature{}, ErrSignatureMalformed
	}
	var out Signature
	seen := map[string]bool{}
	for _, p := range parts[1:] {
		k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
		if !ok || seen[k] {
			return Signature{}, ErrSignatureMalformed
		}
		seen[k] = true
		switch k {
		case "key":
			out.KeyID = v
		case "t":
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return Signature{}, ErrSignatureMalformed
			}
			out.Unix = n
		case "sig":
			raw, err := base64.RawURLEncoding.DecodeString(v)
			if err != nil || len(raw) != ed25519.SignatureSize {
				return Signature{}, ErrSignatureMalformed
			}
			out.Sig = raw
		default:
			// Unknown parameters are ignored so v1 can gain optional fields.
		}
	}
	if out.KeyID == "" || out.Unix == 0 || out.Sig == nil {
		return Signature{}, ErrSignatureMalformed
	}
	return out, nil
}

// Verify checks a parsed signature against a known public key and the request
// as received. The window check runs first so an expired request is reported as
// expired, not invalid: the client action differs (re-sign vs fix).
func Verify(pub ed25519.PublicKey, sig Signature, method, requestURI string, body []byte, now time.Time) error {
	skew := now.Sub(time.Unix(sig.Unix, 0))
	if skew > SignatureWindow || skew < -SignatureWindow {
		return ErrSignatureExpired
	}
	if KeyID(pub) != sig.KeyID {
		return ErrSignatureInvalid
	}
	if !ed25519.Verify(pub, []byte(CanonicalString(method, requestURI, sig.Unix, body)), sig.Sig) {
		return ErrSignatureInvalid
	}
	return nil
}
