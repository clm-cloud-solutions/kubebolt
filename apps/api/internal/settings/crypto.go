package settings

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
)

// secretCrypto encrypts UI-stored secrets (API keys, webhooks) at rest in
// BoltDB. Key is derived from the install's JWT secret via SHA-256 so
// rotating the JWT secret invalidates all encrypted blobs — same
// management surface, single rotation event, no separate secret to track.
//
// AES-256-GCM, stdlib only. Random 12-byte nonce per encryption so
// re-encrypting the same plaintext produces different ciphertext (no
// equality leakage).
type secretCrypto struct {
	aead cipher.AEAD
}

const (
	// minJWTSecretBytes is the lower bound we accept for the source
	// material. JWT secrets at this size give ~128-bit entropy when
	// random-generated, which is acceptable for our threat model
	// (offline BoltDB exfiltration). Shorter is misconfiguration.
	minJWTSecretBytes = 16
)

// secretEnvelope is the on-disk shape: "v1:<base64(nonce || ciphertext)>".
// The version prefix lets us migrate to a new scheme later without
// guessing at format. Plain plaintext (no prefix) is rejected — every
// stored secret must be encrypted.
const secretEnvelopePrefix = "v1:"

// newSecretCrypto builds the AES-GCM AEAD from the JWT secret. Caller
// owns the secret bytes; we copy them into the derived key so they can
// be zeroed at the call site without invalidating the AEAD.
func newSecretCrypto(jwtSecret []byte) (*secretCrypto, error) {
	if len(jwtSecret) < minJWTSecretBytes {
		return nil, fmt.Errorf("settings: jwt secret too short (%d bytes, minimum %d) — secret encryption requires a real secret", len(jwtSecret), minJWTSecretBytes)
	}
	keyArr := sha256.Sum256(jwtSecret)
	block, err := aes.NewCipher(keyArr[:])
	if err != nil {
		return nil, fmt.Errorf("settings: aes init: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("settings: gcm init: %w", err)
	}
	return &secretCrypto{aead: aead}, nil
}

// encrypt wraps plaintext in the versioned envelope. Empty plaintext
// returns empty output (caller's "leave unchanged" sentinel is handled
// upstream — this layer just doesn't store nothing).
func (c *secretCrypto) encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("settings: nonce: %w", err)
	}
	ct := c.aead.Seal(nil, nonce, []byte(plaintext), nil)
	body := append(nonce, ct...)
	return secretEnvelopePrefix + base64.StdEncoding.EncodeToString(body), nil
}

// decrypt unwraps a versioned envelope. An empty input returns empty —
// matches the encrypt contract. A non-empty input without the version
// prefix is rejected as malformed (we never accept plaintext in storage).
//
// Returns errSecretUnreadable specifically when AES-GCM authentication
// fails (the typical "JWT secret rotated, blob is now unreadable"
// situation). Callers can match this error to surface a "re-enter your
// secret" UX instead of a generic 500.
func (c *secretCrypto) decrypt(envelope string) (string, error) {
	if envelope == "" {
		return "", nil
	}
	if len(envelope) <= len(secretEnvelopePrefix) || envelope[:len(secretEnvelopePrefix)] != secretEnvelopePrefix {
		return "", errors.New("settings: malformed secret envelope (no version prefix)")
	}
	body, err := base64.StdEncoding.DecodeString(envelope[len(secretEnvelopePrefix):])
	if err != nil {
		return "", fmt.Errorf("settings: secret base64 decode: %w", err)
	}
	ns := c.aead.NonceSize()
	if len(body) < ns {
		return "", errors.New("settings: secret payload truncated")
	}
	nonce, ct := body[:ns], body[ns:]
	pt, err := c.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		// Most common cause: JWT secret was rotated. Surface a sentinel
		// the caller (handler) can map to a user-friendly UX prompt.
		return "", errSecretUnreadable
	}
	return string(pt), nil
}

// errSecretUnreadable signals that an encrypted secret cannot be decrypted
// with the current key — usually a JWT-secret rotation since the secret
// was written. Handlers inspect this with errors.Is to render the
// "re-enter" recovery flow.
var errSecretUnreadable = errors.New("settings: encrypted secret is unreadable with the current JWT secret — operator likely rotated it; re-enter the value to restore")

// IsSecretUnreadable lets callers (handlers, tests) match the sentinel
// error without importing the unexported variable directly.
func IsSecretUnreadable(err error) bool {
	return errors.Is(err, errSecretUnreadable)
}

// maskSecret produces a UI-safe preview of a secret value, showing
// roughly its provenance without leaking it. Used by GET /settings to
// confirm "yes a key is set" while never round-tripping the cleartext.
// Format: "sk-ant-***6OdZ" — keeps a recognisable prefix and a 4-char
// tail so operators can verify they typed the right key.
func maskSecret(value string) string {
	if value == "" {
		return ""
	}
	if len(value) <= 8 {
		return "***"
	}
	const tailLen = 4
	// Pick a prefix length that captures the kind of key without revealing
	// enough to brute force — typically the provider prefix is the first
	// ~7 chars (sk-ant-, sk-org-, sk-proj-).
	prefixLen := 7
	if len(value) < prefixLen+tailLen+3 {
		// Short keys get an even shorter prefix to keep the mask visible.
		prefixLen = 3
	}
	return value[:prefixLen] + "***" + value[len(value)-tailLen:]
}
