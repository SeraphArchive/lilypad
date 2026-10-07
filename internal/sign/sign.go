// Package sign signs response bodies. Production uses RSA-1024 / PKCS#1 v1.5 /
// SHA-1 over the on-the-wire response body (the scheme the client verifies with
// the embedded server public key, which the client patch replaces with
// LilyPad's). NoopSigner is for verify-disabled internal builds and for routes
// the client accepts unsigned.
package sign

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
)

// Signer signs a response body. ok=false means "emit no x-signature header".
type Signer interface {
	Sign(body []byte) (sig string, ok bool)
}

// NoopSigner never signs.
type NoopSigner struct{}

func (NoopSigner) Sign([]byte) (string, bool) { return "", false }

// RSASigner signs with RSA / PKCS#1 v1.5 / SHA-1.
type RSASigner struct{ priv *rsa.PrivateKey }

// NewRSASigner parses a PEM-encoded RSA private key (PKCS#1 or PKCS#8).
func NewRSASigner(pemKey []byte) (*RSASigner, error) {
	blk, _ := pem.Decode(pemKey)
	if blk == nil {
		return nil, fmt.Errorf("sign: no PEM block in key")
	}
	if key, err := x509.ParsePKCS1PrivateKey(blk.Bytes); err == nil {
		return &RSASigner{priv: key}, nil
	}
	k2, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("sign: parse private key: %w", err)
	}
	rk, ok := k2.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("sign: not an RSA private key")
	}
	return &RSASigner{priv: rk}, nil
}

// Sign returns the base64 PKCS#1 v1.5 SHA-1 signature of body.
func (s *RSASigner) Sign(body []byte) (string, bool) {
	h := sha1.Sum(body)
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.priv, crypto.SHA1, h[:])
	if err != nil {
		return "", false
	}
	return base64.StdEncoding.EncodeToString(sig), true
}

// GenerateKeyPEM creates an RSA key pair and returns PKCS#1 private + PKIX
// public PEM blocks. Used by keygen tooling and tests.
func GenerateKeyPEM(bits int) (privPEM, pubPEM []byte, err error) {
	key, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		return nil, nil, err
	}
	privPEM = pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, nil, err
	}
	pubPEM = pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	return privPEM, pubPEM, nil
}
