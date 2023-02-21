//go:build e2e
// +build e2e

package e2e

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// KeyCert bundles a certificate with its associated private key.
type KeyCert struct {
	// The private key
	Key *rsa.PrivateKey
	// The certificate in logical form
	Cert *x509.Certificate
	// The signed certificate in binary form
	CertPem string
	// The entire certificate chain in binary form
	CertFullChain string
}

// CreateTLSKeyCert creates a key and certificate with CN commonName, valid between notBefore and notAfter, and with CRL
// Distribution Points crlDistributionPoints (if any). If isCA is true, the certificate is marked as being a CA
// certificate. If issuer is non-nil, the pem-encoded certificate is signed by issuer, otherwise it is self signed.
//
// Returns a KeyCert containing the key and certificate in logical form, as well as a pem-encoded version of the
// certificate.
func CreateTLSKeyCert(commonName string, notBefore, notAfter time.Time, isCA bool, crlDistributionPoints []string, issuer *KeyCert) (KeyCert, error) {
	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return KeyCert{}, fmt.Errorf("failed to generate serial number: %w", err)
	}

	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return KeyCert{}, fmt.Errorf("failed to generate key: %w", err)
	}

	subjectKeyId, err := generateSubjectKeyID(privKey)
	if err != nil {
		return KeyCert{}, fmt.Errorf("failed to generate subject key ID: %w", err)
	}

	certificate := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization:       []string{"Openshift E2E Testing"},
			OrganizationalUnit: []string{"Engineering"},
			CommonName:         commonName,
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		IsCA:                  isCA,
		BasicConstraintsValid: true,
		CRLDistributionPoints: crlDistributionPoints,
		SubjectKeyId:          subjectKeyId,
	}

	if isCA {
		certificate.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature
	} else {
		certificate.KeyUsage = x509.KeyUsageDigitalSignature
	}

	// If no issuer is specified, self-sign
	if issuer == nil {
		issuer = &KeyCert{
			Key:  privKey,
			Cert: certificate,
		}
	}

	certBytes, err := x509.CreateCertificate(rand.Reader, certificate, issuer.Cert, &privKey.PublicKey, issuer.Key)
	if err != nil {
		return KeyCert{}, fmt.Errorf("failed to create certificate %s: %w", commonName, err)
	}

	pemBuffer := new(bytes.Buffer)
	if err := pem.Encode(pemBuffer, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certBytes,
	}); err != nil {
		return KeyCert{}, fmt.Errorf("failed to pem encode certificate %s: %w", commonName, err)
	}
	certPem := pemBuffer.String()
	certFullChain := strings.Join([]string{certPem, issuer.CertFullChain}, "")

	return KeyCert{Key: privKey, Cert: certificate, CertPem: certPem, CertFullChain: certFullChain}, nil
}

// MustCreateTLSKeyCert calls CreateTLSKeyCert, but instead of returning an error, it panics if an error occurs
func MustCreateTLSKeyCert(commonName string, notBefore, notAfter time.Time, isCA bool, crlDistributionPoints []string, issuer *KeyCert) KeyCert {
	keyCert, err := CreateTLSKeyCert(commonName, notBefore, notAfter, isCA, crlDistributionPoints, issuer)
	if err != nil {
		panic(err)
	}
	return keyCert
}

// CreateCRL generates a pem-encoded CRL for issuer, valid between thisUpdate and nextUpdate, that lists revokedCerts as
// revoked. Returns the logical form of the CRL, as well as a pem-encoded version.
func CreateCRL(revocationList *x509.RevocationList, issuer KeyCert, thisUpdate, nextUpdate time.Time, revokedCerts []pkix.RevokedCertificate) (*x509.RevocationList, string, error) {
	if revocationList == nil {
		revocationList = &x509.RevocationList{
			Issuer: issuer.Cert.Subject,
			Number: big.NewInt(1),
		}
	} else {
		revocationList.Number.Add(revocationList.Number, big.NewInt(1))
	}
	revocationList.ThisUpdate = thisUpdate
	revocationList.NextUpdate = nextUpdate
	revocationList.RevokedCertificates = revokedCerts

	crlBytes, err := x509.CreateRevocationList(rand.Reader, revocationList, issuer.Cert, issuer.Key)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create CRL for issuer %s: %w", issuer.Cert.Subject.CommonName, err)
	}

	crlBuffer := new(bytes.Buffer)
	if err := pem.Encode(crlBuffer, &pem.Block{
		Type:  "X509 CRL",
		Bytes: crlBytes,
	}); err != nil {
		return nil, "", fmt.Errorf("failed to pem encode CRL for issuer %s: %w", issuer.Cert.Subject.CommonName, err)
	}

	return revocationList, crlBuffer.String(), nil
}

// MustCreateCRL calls CreateCRL, but instead of returning an error, it panics if an error occurs
func MustCreateCRL(revocationList *x509.RevocationList, issuer KeyCert, thisUpdate, nextUpdate time.Time, revokedCerts []pkix.RevokedCertificate) (*x509.RevocationList, string) {
	crl, crlPem, err := CreateCRL(revocationList, issuer, thisUpdate, nextUpdate, revokedCerts)
	if err != nil {
		panic(err)
	}
	return crl, crlPem
}

// RevokeCertificates revokes the certificates in keyCerts at revocationTime, and returns the list of revoked
// certificates, which can be appended to an existing list of revoked certificates and passed to CreateCRL().
func RevokeCertificates(revocationTime time.Time, keyCerts ...KeyCert) []pkix.RevokedCertificate {
	revokedCerts := []pkix.RevokedCertificate{}
	for _, keyCert := range keyCerts {
		revokedCert := pkix.RevokedCertificate{
			RevocationTime: revocationTime,
			SerialNumber:   keyCert.Cert.SerialNumber,
		}
		revokedCerts = append(revokedCerts, revokedCert)
	}
	return revokedCerts
}

// pkcs1PublicKey reflects the ASN.1 structure of a PKCS #1 public key.
type pkcs1PublicKey struct {
	N *big.Int
	E int
}

// generateSubjectKeyID generates a subject key by hashing the ASN.1-encoded public key bit string, as proposed in
// section 4.2.1.2 of RFC-5280.
func generateSubjectKeyID(key *rsa.PrivateKey) ([]byte, error) {
	publicKeyBytes, err := asn1.Marshal(pkcs1PublicKey{
		N: key.PublicKey.N,
		E: key.PublicKey.E,
	})
	if err != nil {
		return nil, err
	}
	subjectKeyId := sha1.Sum(publicKeyBytes)
	return subjectKeyId[:], nil
}
