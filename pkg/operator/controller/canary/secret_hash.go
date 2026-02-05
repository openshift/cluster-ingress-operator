package canary

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	corev1 "k8s.io/api/core/v1"
)



// ComputeTLSSecretHash computes a stable sha256 hex string over the
// relevant keys in a TLS secret. Required keys are `tls.crt` and
// `tls.key`. `ca.crt` is included if present.
func ComputeTLSSecretHash(secret *corev1.Secret) (string, error) {
	if secret == nil {
		return "", fmt.Errorf("secret is nil")
	}

	if secret.Data == nil {
		return "", fmt.Errorf("secret has no data")
	}

	crt, okCrt := secret.Data["tls.crt"]
	key, okKey := secret.Data["tls.key"]
	if !okCrt || !okKey {
		return "", fmt.Errorf("secret missing tls.crt or tls.key")
	}

	hasher := sha256.New()
	hasher.Write(crt)
	hasher.Write([]byte("|"))
	hasher.Write(key)
	if ca, ok := secret.Data["ca.crt"]; ok {
		hasher.Write([]byte("|"))
		hasher.Write(ca)
	}

	sum := hasher.Sum(nil)
	return hex.EncodeToString(sum), nil
}
