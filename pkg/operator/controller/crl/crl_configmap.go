package crl

import (
	"bytes"
	"context"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io/ioutil"
	"net/http"
	"reflect"
	"strings"
	"time"

	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-ingress-operator/pkg/operator/controller"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
)

// authorityKeyIdentifier is a certificate's authority key identifier.
type authorityKeyIdentifier struct {
	KeyIdentifier []byte `asn1:"optional,tag:0"`
}

// authorityKeyIdentifierOID is the ASN.1 object identifier for the authority
// key identifier extension.
var authorityKeyIdentifierOID = asn1.ObjectIdentifier{2, 5, 29, 35}

// ensureCRLConfigmap ensures the client CA certificate revocation list
// configmap exists for a given ingresscontroller if the ingresscontroller
// specifies a client CA certificate bundle in which any certificates specify
// any CRL distribution points.  Returns a Boolean indicating whether the
// configmap exists, the configmap if it does exist, and an error value.
func (r *reconciler) ensureCRLConfigmap(ctx context.Context, ic *operatorv1.IngressController, namespace string, ownerRef metav1.OwnerReference, haveClientCA bool, clientCAConfigmap *corev1.ConfigMap) (bool, *corev1.ConfigMap, context.Context, error) {
	haveCM, current, err := r.currentCRLConfigMap(ctx, ic)
	if err != nil {
		return false, nil, ctx, err
	}

	var oldCRLs map[string]*pkix.CertificateList
	if haveCM {
		if data, ok := current.Data["crl.pem"]; ok {
			if crls, err := buildCRLMap([]byte(data)); err != nil {
				log.Error(err, "failed to parse current client CA configmap", "namespace", current.Namespace, "name", current.Name)
			} else {
				oldCRLs = crls
			}
		}

	}

	var clientCAData []byte
	if haveClientCA {
		clientCABundleFilename := "ca-bundle.pem"
		if data, ok := clientCAConfigmap.Data[clientCABundleFilename]; !ok {
			return haveCM, current, ctx, fmt.Errorf("client CA configmap %s/%s is missing %q", clientCAConfigmap.Namespace, clientCAConfigmap.Name, clientCABundleFilename)
		} else {
			clientCAData = []byte(data)
		}
	}

	wantCM, desired, ctx, err := desiredCRLConfigMap(ctx, ic, ownerRef, clientCAData, oldCRLs)
	if err != nil {
		return false, nil, ctx, fmt.Errorf("failed to build configmap: %w", err)
	}

	switch {
	case !wantCM && !haveCM:
		return false, nil, ctx, nil
	case !wantCM && haveCM:
		if err := r.client.Delete(ctx, current); err != nil {
			if !errors.IsNotFound(err) {
				return true, current, ctx, fmt.Errorf("failed to delete configmap: %w", err)
			}
		} else {
			log.Info("deleted configmap", "namespace", current.Namespace, "name", current.Name)
		}
		return false, nil, ctx, nil
	case wantCM && !haveCM:
		if err := r.client.Create(ctx, desired); err != nil {
			return false, nil, ctx, fmt.Errorf("failed to create configmap: %w", err)
		}
		log.Info("created configmap", "namespace", desired.Namespace, "name", desired.Name)
		exists, current, err := r.currentCRLConfigMap(ctx, ic)
		return exists, current, ctx, err
	case wantCM && haveCM:
		if updated, err := r.updateCRLConfigMap(ctx, current, desired); err != nil {
			return true, current, ctx, fmt.Errorf("failed to update configmap: %w", err)
		} else if updated {
			log.Info("updated configmap", "namespace", desired.Namespace, "name", desired.Name)
			exists, current, err := r.currentCRLConfigMap(ctx, ic)
			return exists, current, ctx, err
		}
	}

	return true, current, ctx, nil
}

// buildCRLMap builds a map of key identifier to certificate list using the
// provided PEM-encoded certificate revocation list.
func buildCRLMap(crlData []byte) (map[string]*pkix.CertificateList, error) {
	crlForKeyId := make(map[string]*pkix.CertificateList)
	for len(crlData) > 0 {
		block, data := pem.Decode(crlData)
		if block == nil {
			break
		}
		crl, err := x509.ParseCRL(block.Bytes)
		if err != nil {
			return crlForKeyId, err
		}
		for _, ext := range crl.TBSCertList.Extensions {
			if ext.Id.Equal(authorityKeyIdentifierOID) {
				var authKeyId authorityKeyIdentifier
				if _, err := asn1.Unmarshal(ext.Value, &authKeyId); err != nil {
					return crlForKeyId, err
				}
				subjectKeyId := hex.EncodeToString(authKeyId.KeyIdentifier)
				crlForKeyId[subjectKeyId] = crl
			}
		}
		crlData = data
	}
	return crlForKeyId, nil
}

// desiredCRLConfigMap returns the desired CRL configmap.  Returns a Boolean
// indicating whether a configmap is desired, the configmap if one is desired,
// the context (containing the next CRL update time as "nextCRLUpdate"), and an
// error if one occurred
func desiredCRLConfigMap(ctx context.Context, ic *operatorv1.IngressController, ownerRef metav1.OwnerReference, clientCAData []byte, crls map[string]*pkix.CertificateList) (bool, *corev1.ConfigMap, context.Context, error) {
	if len(ic.Spec.ClientTLS.ClientCertificatePolicy) == 0 || len(ic.Spec.ClientTLS.ClientCA.Name) == 0 {
		return false, nil, ctx, nil
	}

	if crls == nil {
		crls = make(map[string]*pkix.CertificateList)
	}

	subjectKeyIds := make(map[string]struct{})
	var nextCRLUpdate time.Time
	now := time.Now()
	for len(clientCAData) > 0 {
		block, data := pem.Decode(clientCAData)
		if block == nil {
			break
		}
		clientCAData = data
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return false, nil, ctx, fmt.Errorf("client CA configmap has an invalid certificate: %w", err)
		}
		subjectKeyId := hex.EncodeToString(cert.SubjectKeyId)
		if len(cert.CRLDistributionPoints) == 0 {
			continue
		}
		if crl, ok := crls[subjectKeyId]; ok {
			if crl.HasExpired(now) {
				log.Info("certificate revocation list has expired", "subject key identifier", subjectKeyId)
			} else {
				subjectKeyIds[subjectKeyId] = struct{}{}
				if (nextCRLUpdate.IsZero() || crl.TBSCertList.NextUpdate.Before(nextCRLUpdate)) && crl.TBSCertList.NextUpdate.After(now) {
					nextCRLUpdate = crl.TBSCertList.NextUpdate
				}
				continue
			}
		}
		log.Info("retrieving certificate revocation list", "subject key identifier", subjectKeyId)
		if crl, err := getCRL(cert.CRLDistributionPoints); err != nil {
			// Creating or updating the configmap with incomplete
			// data would compromise security by potentially
			// permitting revoked certificates.
			return false, nil, ctx, fmt.Errorf("failed to get certificate revocation list for certificate key %s: %w", subjectKeyId, err)
		} else {
			crls[subjectKeyId] = crl
			subjectKeyIds[subjectKeyId] = struct{}{}
			log.Info("new certificate revocation list", "subject key identifier", subjectKeyId, "next update", crl.TBSCertList.NextUpdate.String())
			if (nextCRLUpdate.IsZero() || crl.TBSCertList.NextUpdate.Before(nextCRLUpdate)) && crl.TBSCertList.NextUpdate.After(now) {
				nextCRLUpdate = crl.TBSCertList.NextUpdate
			}
		}
	}

	if len(subjectKeyIds) == 0 {
		return false, nil, ctx, nil
	}

	buf := &bytes.Buffer{}
	for subjectKeyId := range subjectKeyIds {
		asn1Data, err := asn1.Marshal(*crls[subjectKeyId])
		if err != nil {
			return false, nil, ctx, fmt.Errorf("failed to encode ASN.1 for CRL for certificate key %s: %w", subjectKeyId, err)
		}
		block := &pem.Block{
			Type:  "X509 CRL",
			Bytes: asn1Data,
		}
		if err := pem.Encode(buf, block); err != nil {
			return false, nil, ctx, fmt.Errorf("failed to encode PEM for CRL for certificate key %s: %w", subjectKeyId, err)
		}
	}
	crlData := buf.String()

	crlConfigmapName := controller.CRLConfigMapName(ic)
	crlConfigmap := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      crlConfigmapName.Name,
			Namespace: crlConfigmapName.Namespace,
		},
		Data: map[string]string{
			"crl.pem": crlData,
		},
	}
	crlConfigmap.SetOwnerReferences([]metav1.OwnerReference{ownerRef})

	return true, &crlConfigmap, context.WithValue(ctx, "nextCRLUpdate", nextCRLUpdate), nil
}

// getCRL gets a certificate revocation list using the provided distribution
// points and returns the certificate list.
func getCRL(distributionPoints []string) (*pkix.CertificateList, error) {
	var errs []error
	for _, distributionPoint := range distributionPoints {
		// The distribution point is typically a URL with the "http"
		// scheme.  "https" is generally not used because the
		// certificate list is signed, and because using TLS to get the
		// certificate list could introduce a circular dependency
		// (cannot use TLS without the revocation list, and cannot get
		// the revocation list without using TLS).
		//
		// TODO Support ldap.
		switch {
		case strings.HasPrefix(distributionPoint, "http:"):
			log.Info("retrieving CRL distribution point", "distribution point", distributionPoint)
			crl, err := getHTTPCRL(distributionPoint)
			if err != nil {
				errs = append(errs, fmt.Errorf("error getting %q: %w", distributionPoint, err))
				continue
			}
			return crl, nil
		default:
			errs = append(errs, fmt.Errorf("unsupported distribution point type: %s", distributionPoint))
		}
	}
	return nil, kerrors.NewAggregate(errs)
}

// getHTTPCRL gets a certificate revocation list using the provided HTTP URL.
func getHTTPCRL(url string) (*pkix.CertificateList, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("http.Get failed: %w", err)
	}
	defer resp.Body.Close()
	bytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading response: %w", err)
	}
	crl, err := x509.ParseCRL(bytes)
	if err != nil {
		return nil, fmt.Errorf("error parsing response: %w", err)
	}
	return crl, nil
}

// currentCRLConfigMap returns the current CRL configmap.  Returns a Boolean
// indicating whether the configmap existed, the configmap if it did exist, and
// an error value.
func (r *reconciler) currentCRLConfigMap(ctx context.Context, ic *operatorv1.IngressController) (bool, *corev1.ConfigMap, error) {
	cm := &corev1.ConfigMap{}
	if err := r.client.Get(ctx, controller.CRLConfigMapName(ic), cm); err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, cm, nil
}

// updateCRLConfigMap updates a configmap.  Returns a Boolean indicating whether
// the configmap was updated, and an error value.
func (r *reconciler) updateCRLConfigMap(ctx context.Context, current, desired *corev1.ConfigMap) (bool, error) {
	if crlConfigmapsEqual(current, desired) {
		return false, nil
	}
	updated := current.DeepCopy()
	updated.Data = desired.Data
	if err := r.client.Update(ctx, updated); err != nil {
		if errors.IsAlreadyExists(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// crlConfigmapsEqual compares two CRL configmaps.  Returns true if the
// configmaps should be considered equal for the purpose of determining whether
// an update is necessary, false otherwise
func crlConfigmapsEqual(a, b *corev1.ConfigMap) bool {
	if !reflect.DeepEqual(a.Data, b.Data) {
		return false
	}
	return true
}
