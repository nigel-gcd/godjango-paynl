package exchange

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"net/http"
	"strings"
)

// Signature headers sent by Pay.nl for signed exchange calls.
const (
	HeaderSignature          = "Signature"
	HeaderSignatureMethod    = "Signature-Method"
	HeaderSignatureAlgorithm = "Signature-Algorithm"
	HeaderSignatureKeyID     = "Signature-KeyId"
)

var (
	ErrMissingSignature = errors.New("paynl/exchange: missing signature")
	ErrInvalidSignature = errors.New("paynl/exchange: invalid signature")
	ErrUnknownKey       = errors.New("paynl/exchange: unknown signature key id")
)

// SecretFunc returns the secret for a signature key id. The key id is the
// token code (AT-...) or service id (SL-...) the call was signed with.
type SecretFunc func(keyID string) (secret string, ok bool)

// StaticSecrets returns a SecretFunc backed by a map of keyID -> secret.
func StaticSecrets(secrets map[string]string) SecretFunc {
	return func(keyID string) (string, bool) {
		s, ok := secrets[keyID]
		return s, ok && s != ""
	}
}

// Verifier verifies signed exchange calls.
type Verifier struct {
	// Secrets resolves the secret used for signing.
	Secrets SecretFunc

	// Required rejects calls which are not signed (e.g. legacy GET calls).
	Required bool
}

// Verify verifies the signature of the request over body.
//
// It returns the key id on success. If the request is not signed and
// Required is false, it returns ("", nil).
func (v *Verifier) Verify(h http.Header, body []byte) (keyID string, err error) {
	var sig = strings.TrimSpace(h.Get(HeaderSignature))
	if sig == "" {
		if v.Required {
			return "", ErrMissingSignature
		}
		return "", nil
	}

	if v.Secrets == nil {
		return "", ErrUnknownKey
	}

	keyID = h.Get(HeaderSignatureKeyID)
	secret, ok := v.Secrets(keyID)
	if !ok {
		return keyID, fmt.Errorf("%w: %q", ErrUnknownKey, keyID)
	}

	if m := h.Get(HeaderSignatureMethod); m != "" && !strings.EqualFold(m, "HMAC") {
		return keyID, fmt.Errorf("paynl/exchange: unsupported signature method %q", m)
	}

	newHash, err := hashFunc(h.Get(HeaderSignatureAlgorithm))
	if err != nil {
		return keyID, err
	}

	if !hmac.Equal([]byte(strings.ToLower(sig)), []byte(Sign(newHash, secret, body))) {
		return keyID, ErrInvalidSignature
	}

	return keyID, nil
}

// Sign computes the hex encoded HMAC of body. Useful for tests.
func Sign(newHash func() hash.Hash, secret string, body []byte) string {
	var mac = hmac.New(newHash, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func hashFunc(algo string) (func() hash.Hash, error) {
	switch strings.ToLower(strings.ReplaceAll(algo, "-", "")) {
	case "", "sha256":
		return sha256.New, nil
	case "sha512":
		return sha512.New, nil
	}
	return nil, fmt.Errorf("paynl/exchange: unsupported signature algorithm %q", algo)
}
