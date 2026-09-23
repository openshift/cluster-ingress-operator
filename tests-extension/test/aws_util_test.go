package test

import (
	"context"
	"crypto/subtle"
	"os"
	"path/filepath"
	"testing"

	configv1 "github.com/openshift/api/config/v1"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// TestLoadAWSConfigPrefersClusterCredentials verifies that the cluster's root
// credentials take precedence over the SDK's default credential chain.
func TestLoadAWSConfigPrefersClusterCredentials(t *testing.T) {
	getSecret := func(_ context.Context, secret *corev1.Secret) error {
		secret.Data = map[string][]byte{
			"aws_access_key_id":     []byte("cluster-access-key"),
			"aws_secret_access_key": []byte("cluster-secret-key"),
		}
		return nil
	}

	cfg, err := loadAWSConfigWithSecretGetter(context.Background(), testAWSInfrastructure(), getSecret)
	if err != nil {
		t.Fatalf("loadAWSConfig returned an error: %v", err)
	}
	credentials, err := cfg.Credentials.Retrieve(context.Background())
	if err != nil {
		t.Fatalf("failed to retrieve credentials: %v", err)
	}
	if credentials.AccessKeyID != "cluster-access-key" {
		t.Error("unexpected access key ID")
	}
	if subtle.ConstantTimeCompare([]byte(credentials.SecretAccessKey), []byte("cluster-secret-key")) != 1 {
		t.Error("unexpected secret access key")
	}
}

// TestLoadAWSConfigUsesDefaultChainWithoutClusterCredentials verifies that the
// SDK's default credential chain is used when the root secret is unavailable.
func TestLoadAWSConfigUsesDefaultChainWithoutClusterCredentials(t *testing.T) {
	credentialsFile := filepath.Join(t.TempDir(), "credentials")
	if err := os.WriteFile(credentialsFile, []byte("[default]\naws_access_key_id = ambient-access-key\naws_secret_access_key = ambient-secret-key\n"), 0600); err != nil {
		t.Fatalf("failed to write credentials file: %v", err)
	}
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", credentialsFile)
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "config"))
	for _, key := range []string{
		"AWS_ACCESS_KEY_ID",
		"AWS_SECRET_ACCESS_KEY",
		"AWS_SESSION_TOKEN",
		"AWS_WEB_IDENTITY_TOKEN_FILE",
		"AWS_ROLE_ARN",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("AWS_PROFILE", "default")

	getSecret := func(_ context.Context, _ *corev1.Secret) error {
		return apierrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, "aws-creds")
	}
	cfg, err := loadAWSConfigWithSecretGetter(context.Background(), testAWSInfrastructure(), getSecret)
	if err != nil {
		t.Fatalf("loadAWSConfig returned an error: %v", err)
	}
	credentials, err := cfg.Credentials.Retrieve(context.Background())
	if err != nil {
		t.Fatalf("failed to retrieve credentials: %v", err)
	}
	if credentials.AccessKeyID != "ambient-access-key" {
		t.Error("unexpected access key ID")
	}
	if subtle.ConstantTimeCompare([]byte(credentials.SecretAccessKey), []byte("ambient-secret-key")) != 1 {
		t.Error("unexpected secret access key")
	}
}

// testAWSInfrastructure returns a minimal AWS infrastructure configuration for tests.
func testAWSInfrastructure() *configv1.Infrastructure {
	return &configv1.Infrastructure{Status: configv1.InfrastructureStatus{
		PlatformStatus: &configv1.PlatformStatus{
			Type: configv1.AWSPlatformType,
			AWS:  &configv1.AWSPlatformStatus{Region: "us-east-1"},
		},
	}}
}
