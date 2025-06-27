package tls

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/sets"
)

// TestIsAllowedCipher verifies that IsAllowedCipher respects allowedCiphers.
func TestIsAllowedCipher(t *testing.T) {
	originalAllowedCiphers := allowedCiphers
	defer func() {
		allowedCiphers = originalAllowedCiphers
	}()

	var (
		// allCiphers was generated using the command
		// `printf '\t\t\t"%s",\n' $(openssl ciphers | tr : '\n')`
		// with the openssl command from openssl-3.2.2-11.fc41.x86_64.
		allCiphers = sets.New[string]([]string{
			"TLS_AES_256_GCM_SHA384",
			"TLS_CHACHA20_POLY1305_SHA256",
			"TLS_AES_128_GCM_SHA256",
			"TLS_AES_128_CCM_SHA256",
			"ECDHE-ECDSA-AES256-GCM-SHA384",
			"ECDHE-RSA-AES256-GCM-SHA384",
			"ECDHE-ECDSA-CHACHA20-POLY1305",
			"ECDHE-RSA-CHACHA20-POLY1305",
			"ECDHE-ECDSA-AES256-CCM",
			"ECDHE-ECDSA-AES128-GCM-SHA256",
			"ECDHE-RSA-AES128-GCM-SHA256",
			"ECDHE-ECDSA-AES128-CCM",
			"ECDHE-ECDSA-AES128-SHA256",
			"ECDHE-RSA-AES128-SHA256",
			"ECDHE-ECDSA-AES256-SHA",
			"ECDHE-RSA-AES256-SHA",
			"ECDHE-ECDSA-AES128-SHA",
			"ECDHE-RSA-AES128-SHA",
			"AES256-GCM-SHA384",
			"AES256-CCM",
			"AES128-GCM-SHA256",
			"AES128-CCM",
			"AES256-SHA256",
			"AES128-SHA256",
			"AES256-SHA",
			"AES128-SHA",
			"DHE-RSA-AES256-GCM-SHA384",
			"DHE-RSA-CHACHA20-POLY1305",
			"DHE-RSA-AES256-CCM",
			"DHE-RSA-AES128-GCM-SHA256",
			"DHE-RSA-AES128-CCM",
			"DHE-RSA-AES256-SHA256",
			"DHE-RSA-AES128-SHA256",
			"DHE-RSA-AES256-SHA",
			"DHE-RSA-AES128-SHA",
			"PSK-AES256-GCM-SHA384",
			"PSK-CHACHA20-POLY1305",
			"PSK-AES256-CCM",
			"PSK-AES128-GCM-SHA256",
			"PSK-AES128-CCM",
			"PSK-AES256-CBC-SHA",
			"PSK-AES128-CBC-SHA256",
			"PSK-AES128-CBC-SHA",
			"DHE-PSK-AES256-GCM-SHA384",
			"DHE-PSK-CHACHA20-POLY1305",
			"DHE-PSK-AES256-CCM",
			"DHE-PSK-AES128-GCM-SHA256",
			"DHE-PSK-AES128-CCM",
			"DHE-PSK-AES256-CBC-SHA",
			"DHE-PSK-AES128-CBC-SHA256",
			"DHE-PSK-AES128-CBC-SHA",
			"ECDHE-PSK-CHACHA20-POLY1305",
			"ECDHE-PSK-AES256-CBC-SHA",
			"ECDHE-PSK-AES128-CBC-SHA256",
			"ECDHE-PSK-AES128-CBC-SHA",
			"RSA-PSK-AES256-GCM-SHA384",
			"RSA-PSK-CHACHA20-POLY1305",
			"RSA-PSK-AES128-GCM-SHA256",
			"RSA-PSK-AES256-CBC-SHA",
			"RSA-PSK-AES128-CBC-SHA256",
			"RSA-PSK-AES128-CBC-SHA",
		}...)
		// fipsCiphers was generated on a FIPS-enabled host using the
		// openssl command from openssl-3.0.7-29.el9_4.x86_64.
		fipsCiphers = sets.New[string]([]string{
			"TLS_AES_256_GCM_SHA384",
			"TLS_AES_128_GCM_SHA256",
			"TLS_AES_128_CCM_SHA256",
			"ECDHE-ECDSA-AES256-GCM-SHA384",
			"ECDHE-RSA-AES256-GCM-SHA384",
			"ECDHE-ECDSA-AES256-CCM",
			"ECDHE-ECDSA-AES128-GCM-SHA256",
			"ECDHE-RSA-AES128-GCM-SHA256",
			"ECDHE-ECDSA-AES128-CCM",
			"DHE-RSA-AES256-GCM-SHA384",
			"DHE-RSA-AES256-CCM",
			"DHE-RSA-AES128-GCM-SHA256",
			"DHE-RSA-AES128-CCM",
			"PSK-AES256-GCM-SHA384",
			"PSK-AES256-CCM",
			"PSK-AES128-GCM-SHA256",
			"PSK-AES128-CCM",
			"DHE-PSK-AES256-GCM-SHA384",
			"DHE-PSK-AES256-CCM",
			"DHE-PSK-AES128-GCM-SHA256",
			"DHE-PSK-AES128-CCM",
		}...)
	)

	testCases := []struct {
		allowedCiphers sets.Set[string]
		cipher         string
		expected       bool
	}{
		{
			allowedCiphers: allCiphers,
			cipher:         "TLS_AES_256_GCM_SHA384",
			expected:       true,
		},
		{
			allowedCiphers: fipsCiphers,
			cipher:         "TLS_AES_256_GCM_SHA384",
			expected:       true,
		},
		{
			allowedCiphers: allCiphers,
			cipher:         "TLS_CHACHA20_POLY1305_SHA256",
			expected:       true,
		},
		{
			allowedCiphers: fipsCiphers,
			cipher:         "TLS_CHACHA20_POLY1305_SHA256",
			expected:       false,
		},
		{
			allowedCiphers: allCiphers,
			cipher:         "bogus",
			expected:       false,
		},
		{
			allowedCiphers: fipsCiphers,
			cipher:         "bogus",
			expected:       false,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.cipher, func(t *testing.T) {
			allowedCiphers = tc.allowedCiphers
			assert.Equal(t, tc.expected, IsAllowedCipher(tc.cipher))
		})
	}
}
