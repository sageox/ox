package sessionstream

import (
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"
	"time"
)

func testKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub, _ := priv.Public().(ed25519.PublicKey)
	return pub, priv
}

func TestSignatureVerifiesExactRequestOnly(t *testing.T) {
	pub, priv := testKey(t)
	now := time.Unix(1_800_000_000, 0)
	uri := AppendURI("cnv_01a1ecc3-a4c4-76c6-8226-b3dba9d29122", "clyr_01a1ecc3-a4c4-76c6-8226-b3dba9d29122", 4096)
	body := []byte("{\"type\":\"user\"}\n")
	sig, err := ParseSignature(Sign(priv, "POST", uri, now, body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := Verify(pub, sig, "POST", uri, body, now); err != nil {
		t.Fatalf("valid signature refused: %v", err)
	}
	cases := []struct {
		name   string
		method string
		uri    string
		body   []byte
		now    time.Time
		want   error
	}{
		{"method", "PUT", uri, body, now, ErrSignatureInvalid},
		{"path names another conversation", "POST", strings.Replace(uri, "cnv_01a1ecc3", "cnv_01a1ecc4", 1), body, now, ErrSignatureInvalid},
		{"offset", "POST", AppendURI("cnv_x", "clyr_x", 0), body, now, ErrSignatureInvalid},
		{"body", "POST", uri, []byte("{\"type\":\"assistant\"}\n"), now, ErrSignatureInvalid},
		{"too old", "POST", uri, body, now.Add(SignatureWindow + time.Second), ErrSignatureExpired},
		{"from the future", "POST", uri, body, now.Add(-SignatureWindow - time.Second), ErrSignatureExpired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := Verify(pub, sig, tc.method, tc.uri, tc.body, tc.now); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestSignatureRejectsAnotherKey(t *testing.T) {
	_, priv := testKey(t)
	otherPub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	sig, _ := ParseSignature(Sign(priv, "POST", PathKeys, now, nil))
	if err := Verify(otherPub, sig, "POST", PathKeys, nil, now); !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("got %v, want invalid", err)
	}
}

func TestParseSignatureRefusesMalformedHeaders(t *testing.T) {
	_, priv := testKey(t)
	good := Sign(priv, "GET", "/x", time.Now(), nil)
	for name, h := range map[string]string{
		"empty":         "",
		"wrong version": strings.Replace(good, "v1", "v2", 1),
		"no key":        "v1; t=1; sig=" + strings.Split(good, "sig=")[1],
		"bad sig":       "v1; key=abc; t=1; sig=!!",
		"duplicate key": good + "; key=other",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseSignature(h); err == nil {
				t.Fatal("malformed header accepted")
			}
		})
	}
	if _, err := ParseSignature(good + "; future=1"); err != nil {
		t.Fatalf("unknown parameter must be ignored: %v", err)
	}
}

func TestPublicKeyRoundTripAndKeyIDIsStable(t *testing.T) {
	pub, _ := testKey(t)
	back, err := DecodePublicKey(EncodePublicKey(pub))
	if err != nil || !back.Equal(pub) {
		t.Fatalf("round trip failed: %v", err)
	}
	if KeyID(pub) != KeyID(back) || len(KeyID(pub)) != 22 {
		t.Fatalf("key id unstable or wrong length: %q", KeyID(pub))
	}
	if _, err := DecodePublicKey("c2hvcnQ"); err == nil {
		t.Fatal("short key accepted")
	}
}
