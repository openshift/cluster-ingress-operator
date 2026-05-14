package aws

import (
	"bytes"
	"fmt"
	"os"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
)

// SharedCredentialsFileFromSecret returns a location (path) to the shared
// credentials file that was created using the provided secret.
//
// Consumers can configure the aws config using that file to use the
// credentials as follows:
//
//	sharedCredentialsFile, err := SharedCredentialsFileFromSecret(secret)
//	if err != nil {
//		// handle error
//	}
//
//	cfg, err := config.LoadDefaultConfig(ctx,
//		config.WithSharedCredentialsFiles([]string{sharedCredentialsFile}),
//		config.WithSharedConfigFiles([]string{sharedCredentialsFile}),
//	)
func SharedCredentialsFileFromSecret(secret *corev1.Secret) (string, error) {
	var data []byte
	switch {
	case len(secret.Data["credentials"]) > 0:
		data = secret.Data["credentials"]
	case len(secret.Data["aws_access_key_id"]) > 0 && len(secret.Data["aws_secret_access_key"]) > 0:
		data = newConfigForStaticCreds(
			string(secret.Data["aws_access_key_id"]),
			string(secret.Data["aws_secret_access_key"]),
		)

	default:
		return "", errors.New("invalid secret for aws credentials")
	}

	f, err := os.CreateTemp("", "aws-shared-credentials")
	if err != nil {
		return "", errors.Wrap(err, "failed to create file for shared credentials")
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return "", errors.Wrapf(err, "failed to write credentials to %s", f.Name())
	}
	return f.Name(), nil
}

func newConfigForStaticCreds(accessKey string, accessSecret string) []byte {
	buf := &bytes.Buffer{}
	fmt.Fprint(buf, "[default]\n")
	fmt.Fprintf(buf, "aws_access_key_id = %s\n", accessKey)
	fmt.Fprintf(buf, "aws_secret_access_key = %s\n", accessSecret)
	return buf.Bytes()
}
