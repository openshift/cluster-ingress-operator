package canary

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func Test_ComputeTLSSecretHash(t *testing.T) {
	base := &corev1.Secret{
		Data: map[string][]byte{
			"tls.crt": []byte("cert-data"),
			"tls.key": []byte("key-data"),
		},
	}

	same := &corev1.Secret{
		Data: map[string][]byte{
			"tls.crt": []byte("cert-data"),
			"tls.key": []byte("key-data"),
		},
	}

	different := &corev1.Secret{
		Data: map[string][]byte{
			"tls.crt": []byte("other-cert"),
			"tls.key": []byte("key-data"),
		},
	}

	withCA := &corev1.Secret{
		Data: map[string][]byte{
			"tls.crt": []byte("cert-data"),
			"tls.key": []byte("key-data"),
			"ca.crt":  []byte("ca-data"),
		},
	}

	h1, err := ComputeTLSSecretHash(base)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	h2, err := ComputeTLSSecretHash(same)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h1 != h2 {
		t.Fatalf("expected same hash for identical secrets: %s != %s", h1, h2)
	}

	hd, err := ComputeTLSSecretHash(different)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h1 == hd {
		t.Fatalf("expected different hash for different secret data")
	}

	hca, err := ComputeTLSSecretHash(withCA)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h1 == hca {
		t.Fatalf("expected different hash when ca.crt is present")
	}
}
