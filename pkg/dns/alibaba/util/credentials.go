package util

import (
	"fmt"
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/auth/credentials"
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/auth/credentials/provider"
	"io/ioutil"
	corev1 "k8s.io/api/core/v1"
	"os"
	"sync"
)

var (
	mutex sync.Mutex
)

// Credentials contains AccessKeyID and AccessKeySecret to grant access to AlibabaCloud OpenAPI
type Credentials struct {
	AccessKeyID     string
	AccessKeySecret string
}

// FetchAlibabaCredentialsIniFromSecret fetches secret from cloud credentials and returns Credentials
// which provides access key & secret key.
func FetchAlibabaCredentialsIniFromSecret(secret *corev1.Secret) (*Credentials, error) {
	creds, ok := secret.Data["credentials"]
	if !ok {
		return nil, fmt.Errorf("failed to fetch key 'credentials' in secret data")
	}
	f, err := ioutil.TempFile("", "alibaba-creds-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	defer f.Close()

	_, err = f.Write(creds)
	if err != nil {
		return nil, err
	}
	// This lock is used to prevent the environment variable from being updated while we
	// are using the environment variable to call the Alibaba credential provider chain.
	mutex.Lock()
	defer mutex.Unlock()
	os.Setenv(provider.ENVCredentialFile, f.Name())
	defer os.Unsetenv(provider.ENVCredentialFile)
	// use Alibaba provider initialization
	p := provider.NewProfileProvider("default")
	// get a valid auth credential
	authCred, err := p.Resolve()
	if err != nil {
		return nil, fmt.Errorf("failed to get alibabacloud auth credentials: %w", err)
	}

	c, ok := authCred.(*credentials.AccessKeyCredential)
	if !ok {
		return nil, fmt.Errorf("failed to convert the credential to an AccessKeyCredential")
	}

	return &Credentials{
		AccessKeyID:     c.AccessKeyId,
		AccessKeySecret: c.AccessKeySecret,
	}, nil
}
