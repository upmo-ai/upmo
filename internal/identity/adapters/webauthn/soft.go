package webauthn

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"

	"github.com/fxamacker/cbor/v2"
	"github.com/google/uuid"
)

// Soft is a passkey that lives in memory. Tests use it in place of a browser.
type Soft struct {
	key    *ecdsa.PrivateKey
	id     []byte
	aaguid uuid.UUID
}

// NewSoft creates a passkey.
func NewSoft() (*Soft, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return nil, err
	}
	return &Soft{key: key, id: id, aaguid: uuid.MustParse("fbfc3007-154e-4ecc-8c0b-6e020557d7bd")}, nil
}

// Register answers a creation ceremony.
func (s *Soft) Register(rpID, origin, challenge string, _ []byte) ([]byte, error) {
	auth := s.authData(rpID, true)
	client := clientData("webauthn.create", challenge, origin)
	object, err := cbor.Marshal(map[string]any{
		"fmt":      "none",
		"attStmt":  map[string]any{},
		"authData": auth,
	})
	if err != nil {
		return nil, err
	}
	return json.Marshal(credentialJSON{
		ID:    b64(s.id),
		RawID: b64(s.id),
		Type:  "public-key",
		Response: map[string]string{
			"clientDataJSON":    b64(client),
			"attestationObject": b64(object),
		},
	})
}

// Login answers an assertion ceremony. The sign count stays 0, as a synced passkey does.
func (s *Soft) Login(rpID, origin, challenge string, userHandle []byte) ([]byte, error) {
	auth := s.authData(rpID, false)
	client := clientData("webauthn.get", challenge, origin)
	sum := sha256.Sum256(client)
	signed := sha256.Sum256(append(append([]byte{}, auth...), sum[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, s.key, signed[:])
	if err != nil {
		return nil, err
	}
	return json.Marshal(credentialJSON{
		ID:    b64(s.id),
		RawID: b64(s.id),
		Type:  "public-key",
		Response: map[string]string{
			"clientDataJSON":    b64(client),
			"authenticatorData": b64(auth),
			"signature":         b64(sig),
			"userHandle":        b64(userHandle),
		},
	})
}

func (s *Soft) authData(rpID string, create bool) []byte {
	sum := sha256.Sum256([]byte(rpID))
	flags := byte(0x01 | 0x04 | 0x08 | 0x10) // UP, UV, BE, BS
	out := append(sum[:], flags, 0, 0, 0, 0)
	if !create {
		return out
	}
	out[32] |= 0x40
	cose := coseKey(s.key)
	out = append(out, s.aaguid[:]...)
	out = append(out, 0, 16)
	out = append(out, s.id...)
	return append(out, cose...)
}

func coseKey(key *ecdsa.PrivateKey) []byte {
	encoded, err := cbor.Marshal(map[int]any{
		1:  2,
		3:  -7,
		-1: 1,
		-2: pad32(key.X.Bytes()),
		-3: pad32(key.Y.Bytes()),
	})
	if err != nil {
		panic(err)
	}
	return encoded
}

func pad32(b []byte) []byte {
	if len(b) >= 32 {
		return b
	}
	out := make([]byte, 32)
	copy(out[32-len(b):], b)
	return out
}

func clientData(kind, challenge, origin string) []byte {
	raw, _ := json.Marshal(map[string]any{
		"type":        kind,
		"challenge":   challenge,
		"origin":      origin,
		"crossOrigin": false,
	})
	return raw
}

func b64(raw []byte) string {
	return base64.RawURLEncoding.EncodeToString(raw)
}

type credentialJSON struct {
	ID       string            `json:"id"`
	RawID    string            `json:"rawId"`
	Type     string            `json:"type"`
	Response map[string]string `json:"response"`
}
