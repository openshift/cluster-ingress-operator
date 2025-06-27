package tls

import (
	"os/exec"
	"strings"

	"k8s.io/apimachinery/pkg/util/sets"
)

var (
	// allowedCiphers is a list of ciphers that OpenSSL allows.
	allowedCiphers = sets.New[string](func() []string {
		out, err := exec.Command("/bin/openssl", "ciphers").Output()
		if err != nil {
			panic(err)
		}

		return strings.Split(strings.TrimSpace(string(out)), ":")
	}()...)
)

// IsAllowedCipher returns a Boolean value indicating whether OpenSSL supports
// the specified cipher name.
func IsAllowedCipher(cipherName string) bool {
	return allowedCiphers.Has(cipherName)
}
