// Package bridgelink generates Reality keys and encodes/decodes a bridge link.
//
// A link is a compact description of one entry point: host, port, Reality public key, short_id, SNI
// and capability flags. In private mode it is shared by hand; with auto-publishing the same fields
// go to the control server. The Ringly app understands the ringly-bridge:// scheme.
package bridgelink

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// Scheme — the bridge link prefix.
const Scheme = "ringly-bridge://"

// Flow — the VLESS flow for Reality (fixed).
const Flow = "xtls-rprx-vision"

// Keys — generated VLESS+Reality identifiers.
type Keys struct {
	PrivateKey string // X25519 private, base64url raw
	PublicKey  string // X25519 public, base64url raw
	ShortID    string // hex, 8 bytes
	UUID       string // VLESS user id (UUIDv4)
}

// GenerateKeys creates an X25519 pair for Reality, a random short_id and the VLESS user UUID.
func GenerateKeys() (Keys, error) {
	curve := ecdh.X25519()
	priv, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return Keys{}, fmt.Errorf("generating the Reality key: %w", err)
	}
	sid := make([]byte, 8)
	if _, err := rand.Read(sid); err != nil {
		return Keys{}, fmt.Errorf("generating short_id: %w", err)
	}
	uuid, err := generateUUID()
	if err != nil {
		return Keys{}, err
	}
	enc := base64.RawURLEncoding
	return Keys{
		PrivateKey: enc.EncodeToString(priv.Bytes()),
		PublicKey:  enc.EncodeToString(priv.PublicKey().Bytes()),
		ShortID:    hex.EncodeToString(sid),
		UUID:       uuid,
	}, nil
}

// GenerateSecret returns a random secret (32 bytes, hex) for TURN REST authentication.
func GenerateSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating the secret: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// generateUUID returns a random UUIDv4 in the canonical 8-4-4-4-12 form.
func generateUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating the UUID: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// PublicFromPrivate derives the Reality public key from the private one (for `link` and registration:
// the private key is in the configuration, the public one is never stored).
func PublicFromPrivate(privB64 string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(privB64)
	if err != nil {
		return "", fmt.Errorf("decoding the private key: %w", err)
	}
	priv, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return "", fmt.Errorf("invalid Reality private key: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()), nil
}

// Descriptor — what is encoded into the link and sent at registration.
type Descriptor struct {
	Host      string `json:"h"`             // the daemon's public host/IP
	Port      int    `json:"p"`             // bridge port (bridge.listen)
	PublicKey string `json:"pk"`            // Reality public key (base64url)
	ShortID   string `json:"sid"`           // hex
	SNI       string `json:"sni"`           // borrowed cover domain
	UUID      string `json:"id"`            // VLESS user
	Msg       bool   `json:"msg"`           // offers the message bridge
	Calls     bool   `json:"call"`          // offers the call relay
	Geo       string `json:"geo,omitempty"` // ISO country code, for geo matching (optional)
}

// Encode encodes the descriptor into a ringly-bridge://<base64url(json)> link.
func Encode(d Descriptor) (string, error) {
	raw, err := json.Marshal(d)
	if err != nil {
		return "", fmt.Errorf("serializing the descriptor: %w", err)
	}
	return Scheme + base64.RawURLEncoding.EncodeToString(raw), nil
}

// Decode parses a bridge link back into a descriptor.
func Decode(link string) (Descriptor, error) {
	s := strings.TrimSpace(link)
	if !strings.HasPrefix(s, Scheme) {
		return Descriptor{}, fmt.Errorf("not a bridge link (expected the %s prefix)", Scheme)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, Scheme))
	if err != nil {
		return Descriptor{}, fmt.Errorf("decoding the link: %w", err)
	}
	var d Descriptor
	if err := json.Unmarshal(raw, &d); err != nil {
		return Descriptor{}, fmt.Errorf("parsing the descriptor: %w", err)
	}
	return d, nil
}
