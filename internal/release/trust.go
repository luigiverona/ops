package release

import (
	_ "embed"
	"strings"
)

//go:embed signing-fingerprint
var signingFingerprintFile string

//go:embed signing-key.asc
var signingPublicKeyFile string

func DefaultTrust() Trust {
	return Trust{
		Fingerprint: strings.TrimSpace(signingFingerprintFile),
		PublicKey:   signingPublicKeyFile,
	}
}
