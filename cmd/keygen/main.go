// Command keygen generates the LilyPad response-signing RSA key pair. The
// private key configures the server's RSA1024Sha1Signer; the public key
// replaces the client's embedded server public key (see clientpatch/README.md).
//
// Usage: keygen [-bits 1024] [-out dir]
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"lilypad/internal/sign"
)

func main() {
	bits := flag.Int("bits", 1024, "RSA key size (client verifies RSA-1024)")
	out := flag.String("out", "secrets", "output directory for the key pair")
	flag.Parse()

	priv, pub, err := sign.GenerateKeyPEM(*bits)
	if err != nil {
		fmt.Fprintln(os.Stderr, "keygen:", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(*out, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "keygen:", err)
		os.Exit(1)
	}
	privPath := filepath.Join(*out, "lilypad_signing_private.pem")
	pubPath := filepath.Join(*out, "lilypad_signing_public.pem")
	if err := os.WriteFile(privPath, priv, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "keygen:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(pubPath, pub, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "keygen:", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s (set signing.private_key_pem to this, mode: rsa)\n", privPath)
	fmt.Printf("wrote %s (patch into the client's ServerPublicKey)\n", pubPath)
	fmt.Println("NOTE: keep the private key out of git (.pem is gitignored).")
}
