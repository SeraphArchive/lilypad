package sign

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"testing"
)

func TestNoopSigner(t *testing.T) {
	if _, ok := (NoopSigner{}).Sign([]byte("x")); ok {
		t.Fatal("noop must not sign")
	}
}

func TestRSASignerVerifies(t *testing.T) {
	priv, pub, err := GenerateKeyPEM(1024)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewRSASigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("response-body-bytes")
	sigB64, ok := s.Sign(body)
	if !ok {
		t.Fatal("rsa signer must sign")
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		t.Fatal(err)
	}
	if len(sig) != 128 {
		t.Fatalf("RSA-1024 sig must be 128 bytes, got %d", len(sig))
	}
	blk, _ := pem.Decode(pub)
	pk, err := x509.ParsePKIXPublicKey(blk.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	h := sha1.Sum(body)
	if err := rsa.VerifyPKCS1v15(pk.(*rsa.PublicKey), crypto.SHA1, h[:], sig); err != nil {
		t.Fatalf("verify failed: %v", err)
	}
}

func TestNewRSASignerRejectsGarbage(t *testing.T) {
	if _, err := NewRSASigner([]byte("not a pem")); err == nil {
		t.Fatal("expected error on non-PEM input")
	}
}

func TestSignerInterface(t *testing.T) {
	var _ Signer = NoopSigner{}
	priv, _, _ := GenerateKeyPEM(1024)
	s, _ := NewRSASigner(priv)
	var _ Signer = s
}
