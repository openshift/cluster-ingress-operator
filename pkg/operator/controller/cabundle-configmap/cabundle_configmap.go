package cabundleconfigmap

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	// adminCABundleConfigMapKeyName is the name of the key holding CA certificates
	// in the admin-ca-bundle.
	adminCABundleConfigMapKeyName = "ca-bundle.crt"

	// serviceCABundleConfigMapKeyName is the name of the key holding CA certificates
	// in the service-ca-bundle.
	serviceCABundleConfigMapKeyName = "service-ca.crt"

	// ingressCABundleConfigMapKeyName is the name of the key holding CA certificates
	// in the ingress-ca-bundle.
	ingressCABundleConfigMapKeyName = "ca-bundle.crt"
)

var (
	// insecureCertificateSignatureAlgorithms is used to warn about and
	// filter out certificates that use algorithms that are no longer
	// supported by OpenSSL. Configuring the router with a certificate that
	// used one of these algorithms would cause HAProxy to refuse to start.
	insecureCertificateSignatureAlgorithms = map[x509.SignatureAlgorithm]string{
		x509.UnknownSignatureAlgorithm: x509.UnknownSignatureAlgorithm.String(),
		x509.MD2WithRSA:                x509.MD2WithRSA.String(),
		x509.MD5WithRSA:                x509.MD5WithRSA.String(),
		x509.SHA1WithRSA:               x509.SHA1WithRSA.String(),
		x509.DSAWithSHA1:               x509.DSAWithSHA1.String(),
		x509.DSAWithSHA256:             x509.DSAWithSHA256.String(),
		x509.ECDSAWithSHA1:             x509.ECDSAWithSHA1.String(),
	}
)

// ensureIngressCABundleConfigMap syncs ingress CA bundle configmap. Returns
// an error value.
func (r *reconciler) ensureIngressCABundleConfigMap(ctx context.Context) error {
	have, current, err := r.currentConfigMap(ctx, r.config.IngressCAConfigMapName)
	if err != nil {
		return err
	}

	want, desired, err := r.desiredIngressCABundleConfigMap(ctx)
	if err != nil {
		return err
	}

	switch {
	case want && !have:
		if err := r.client.Create(ctx, desired); err != nil {
			return fmt.Errorf("failed to create %s configmap: %w", desired.Name, err)
		}
		log.Info("created configmap", "namespace", desired.Namespace, "name", desired.Name)
		return nil
	case want && have:
		if updated, err := r.updateCABundleConfigMap(ctx, current, desired); err != nil {
			return fmt.Errorf("failed to update %s configmap: %w", desired.Name, err)
		} else if updated {
			log.Info("updated configmap", "namespace", desired.Namespace, "name", desired.Name)
			return nil
		}
	}
	return nil
}

// desiredIngressCABundleConfigMap returns the desired ingress CA bundle configmap. Returns a
// Boolean indicating whether a configmap is desired, the configmap if one is desired and
// an error value.
func (r *reconciler) desiredIngressCABundleConfigMap(ctx context.Context) (bool, *corev1.ConfigMap, error) {
	_, adminCABundle, err := r.currentConfigMap(ctx, r.config.AdminCAConfigMapName)
	if err != nil {
		return false, nil, err
	}

	exist, serviceCABundle, err := r.currentConfigMap(ctx, r.config.ServiceCAConfigMapName)
	if err != nil {
		return false, nil, err
	}
	// Earlier implementation where, defaultDestinationCA had just OpenShift service CA and
	// required service-ca-bundle to exist for the router deployment to start. And to keep
	// the same behavior ingress-ca-bundle will not be created when service-ca-bundle does not exist.
	if !exist {
		return false, nil, fmt.Errorf("openshift service CA bundle does not exist, name: %+v", r.config.ServiceCAConfigMapName)
	}

	ingressCABundleData := make(map[string]string, 1)
	certBuffer := new(bytes.Buffer)
	if serviceCABundle != nil {
		if serviceCABundle.Data == nil {
			return false, nil, fmt.Errorf("%s does not contain any config data", serviceCABundle.Name)
		}
		data, exist := serviceCABundle.Data[serviceCABundleConfigMapKeyName]
		if !exist {
			return false, nil, fmt.Errorf("%s does not contain \"%s\" key", serviceCABundle.Name, serviceCABundleConfigMapKeyName)
		}
		parsedCerts, validCerts, err := r.sanitizeCACertificateBundle(serviceCABundle.Name, []byte(data), certBuffer)
		if err != nil {
			return false, nil, fmt.Errorf("failed to validate %s config data", serviceCABundle.Name)
		}
		log.Info("successfully validated CA certificate bundle", "name", serviceCABundle.Name, "parsed certificates count", parsedCerts, "discarded certificates count", parsedCerts-validCerts)
	}
	if adminCABundle != nil && adminCABundle.Data != nil {
		data, exist := adminCABundle.Data[adminCABundleConfigMapKeyName]
		if exist {
			parsedCerts, validCerts, err := r.sanitizeCACertificateBundle(adminCABundle.Name, []byte(data), certBuffer)
			if err != nil {
				r.eventRecorder.Warningf("CABundleValidation", "failed to validate %s config data: %v", adminCABundle.Name, err)
			}
			log.Info("successfully validated CA certificate bundle", "name", adminCABundle.Name, "parsed certificates count", parsedCerts, "discarded certificates count", parsedCerts-validCerts)
		} else {
			r.eventRecorder.Warningf("CABundleValidation", "%s is invalid, must contain \"%s\" key with required CA certificates", adminCABundle.Name, adminCABundleConfigMapKeyName)
		}
	}
	ingressCABundleData[ingressCABundleConfigMapKeyName] = certBuffer.String()

	ingressCABundleName := r.config.IngressCAConfigMapName
	cm := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ingressCABundleName.Name,
			Namespace: ingressCABundleName.Namespace,
		},
		Data: ingressCABundleData,
	}
	return true, &cm, nil
}

// currentConfigMap returns the current named configmap. Returns a Boolean
// indicating whether the configmap existed, the configmap if it did exist,
// and an error value.
func (r *reconciler) currentConfigMap(ctx context.Context, name types.NamespacedName) (bool, *corev1.ConfigMap, error) {
	var cm corev1.ConfigMap
	if err := r.client.Get(ctx, name, &cm); err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, &cm, nil
}

// updateCABundleConfigMap updates a configmap. Returns a Boolean indicating
// whether the configmap was updated, and an error value.
func (r *reconciler) updateCABundleConfigMap(ctx context.Context, current, desired *corev1.ConfigMap) (bool, error) {
	if caBundleConfigmapsEqual(current, desired) {
		return false, nil
	}
	updated := current.DeepCopy()
	updated.Data = desired.Data
	if err := r.client.Update(ctx, updated); err != nil {
		return false, err
	}
	return true, nil
}

// caBundleConfigmapsEqual compares two CA bundle configmaps.  Returns true if
// the configmaps should be considered equal for the purpose of determining
// whether an update is necessary, false otherwise.
func caBundleConfigmapsEqual(a, b *corev1.ConfigMap) bool {
	return reflect.DeepEqual(a.Data, b.Data)
}

// encodeSerialNumber encodes certificate serial number back into hex format
// which was decoded during certificate parsing. Serial number is encoded to
// exact representation as observed in the certificates to make certificate
// identification easier.
func encodeSerialNumber(serialNumber big.Int) string {
	var serialNumberWithColon string
	serialNumberStr := hex.EncodeToString(serialNumber.Bytes())
	for i := 0; i < len(serialNumberStr); i++ {
		if i != 0 && i%2 == 0 {
			serialNumberWithColon += ":"
		}
		serialNumberWithColon += string(serialNumberStr[i])
	}
	return serialNumberWithColon
}

// getCertificatePrintId returns an identifier in the format
// <Certificate SerialNumber>(SerialNumber)-<Certificate CommonName>(CommonName)
// which is used in logging and for better identification of the certificate in
// the bundle.  For example:
// 44:81:8b:e8:69:bd:69:34:5c:7e:9c:66:83:ec:01:6c:3e:79:51:9d(SerialNumber)-admin-ca(CommonName)
// certificate in the bundle.
func getCertificatePrintId(cert *x509.Certificate) string {
	return fmt.Sprintf("%s(SerialNumber)-%s(CommonName)", encodeSerialNumber(*cert.SerialNumber), cert.Subject.CommonName)
}

// sanitizeCACertificateBundle validates all the certificates present in the CA
// bundle, removing duplicates.
func (r *reconciler) sanitizeCACertificateBundle(caCertBundleName string, caCertBundle []byte, certBuffer *bytes.Buffer) (int, int, error) {
	// Parse PEM data.
	parsedCerts := make([]*x509.Certificate, 0)
	for block, rest := pem.Decode(caCertBundle); block != nil; block, rest = pem.Decode(rest) {
		switch block.Type {
		case "CERTIFICATE":
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				return len(parsedCerts), 0, fmt.Errorf("failed to parse CA certificate in %s bundle: %w", caCertBundleName, err)
			}
			parsedCerts = append(parsedCerts, cert)
		default:
			log.Error(nil, "CA certificate bundle contains non-certificate data, discarded", "ca bundle name", caCertBundleName, "type", block.Type)
			continue
		}
	}

	// Validate certificates.
	validCerts := make([]*x509.Certificate, 0)
	for i := range parsedCerts {
		if skip := r.validateCACertificate(parsedCerts[i], caCertBundleName); !skip {
			validCerts = append(validCerts, parsedCerts[i])
		}
	}

	// Check for duplicates.
	certMap := map[string]*x509.Certificate{}
	for i := range validCerts {
		cert := validCerts[i]
		k := fmt.Sprintf("%v:%x:%s", *cert.SerialNumber, cert.SubjectKeyId, cert.Subject.CommonName)
		if _, ok := certMap[k]; ok {
			log.Error(nil, "duplicate certificates found", "certificate", getCertificatePrintId(cert))
		} else {
			certMap[k] = cert
		}
	}

	// Marshal valid certificates (including any duplicates) back to PEM.
	for i := range validCerts {
		block := &pem.Block{
			Type:  "CERTIFICATE",
			Bytes: validCerts[i].Raw,
		}
		if err := pem.Encode(certBuffer, block); err != nil {
			return len(parsedCerts), len(validCerts),
				fmt.Errorf("failed to encode %s certificate of %s bundle: %w", getCertificatePrintId(validCerts[i]), caCertBundleName, err)
		}
	}

	return len(parsedCerts), len(validCerts), nil
}

// validateCACertificate does below validations on a CA certificate.
// - Secure Signature Algorithm is made use of for certificate signing.
// - Certificate Validity.
// - Certificate is a Certificate Authority.
func (r *reconciler) validateCACertificate(cert *x509.Certificate, caCertBundleName string) bool {
	skip := false
	certID := getCertificatePrintId(cert)
	if signAlgoName, ok := insecureCertificateSignatureAlgorithms[cert.SignatureAlgorithm]; ok {
		r.eventRecorder.Warningf("CABundleValidation", "certificate uses insecure signature algorithm, discarded. { certificate: %s, signature algorithm: %s, certificate bundle: %s }", certID, signAlgoName, caCertBundleName)
		skip = true
	}

	if !cert.IsCA {
		r.eventRecorder.Warningf("CABundleValidation", "certificate is not a CA certificate, discarded. { certificate: %s, certificate bundle: %s }", certID, caCertBundleName)
		skip = true
	}

	curTime := time.Now()
	if curTime.Sub(cert.NotBefore).Hours() < 0 {
		r.eventRecorder.Warningf("CABundleValidation", "certificate is not yet valid. { certificate: %s, valid after: %s, certificate bundle: %s }", certID, cert.NotBefore.String(), caCertBundleName)
	}
	if curTime.Sub(cert.NotAfter).Hours() > 0 {
		r.eventRecorder.Warningf("CABundleValidation", "certificate has expired. { certificate: %s, expired on: %s, certificate bundle: %s }", certID, cert.NotAfter.String(), caCertBundleName)
	}

	return skip
}
