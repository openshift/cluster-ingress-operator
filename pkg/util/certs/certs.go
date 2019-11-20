package certs

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
)

// makeTLSConfig returns a TLS configuration with a CA
// pool containing certificates from certs.
func makeTLSConfig(certs []*x509.Certificate) *tls.Config {
	caPool := x509.NewCertPool()
	for _, cert := range certs {
		caPool.AddCert(cert)
	}
	return &tls.Config{RootCAs: caPool}
}

// MakeTLSTransport returns an HTTP transport with a TLS
// configuration containing CA certificates from certs.
func MakeTLSTransport(certs []*x509.Certificate) *http.Transport {
	cfg := makeTLSConfig(certs)
	return &http.Transport{TLSClientConfig: cfg}
}

// CertsFromPEM parses pemCerts into a list of certificates.
func CertsFromPEM(pemCerts []byte) ([]*x509.Certificate, error) {
	ok := false
	certs := []*x509.Certificate{}
	for len(pemCerts) > 0 {
		var block *pem.Block
		block, pemCerts = pem.Decode(pemCerts)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			continue
		}

		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return certs, err
		}

		certs = append(certs, cert)
		ok = true
	}

	if !ok {
		return certs, errors.New("could not read any certificates")
	}
	return certs, nil
}
