package aws

import (
	"io/ioutil"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
)

func Test_newConfigForStaticCreds(t *testing.T) {
	cases := []struct {
		key, secret  string
		sharedConfig string
	}{{
		key:    `SOMETHING`,
		secret: `som@thin"g`,

		sharedConfig: `[default]
aws_access_key_id = SOMETHING
aws_secret_access_key = som@thin"g
`,
	}, {
		key:    `ANOTHERTHING`,
		secret: `som@th'n#g+/`,

		sharedConfig: `[default]
aws_access_key_id = ANOTHERTHING
aws_secret_access_key = som@th'n#g+/
`,
	}}
	for _, test := range cases {
		t.Run("", func(t *testing.T) {
			sharedConfig := newConfigForStaticCreds(test.key, test.secret)
			assert.Equal(t, string(sharedConfig), test.sharedConfig)
		})
	}
}

func Test_SharedCredentialsFileFromSecret(t *testing.T) {
	cases := []struct {
		data map[string]string

		sharedConfig string
		err          string
	}{{
		data: map[string]string{
			"aws_access_key_id":     "SOMETHING",
			"aws_secret_access_key": "ANOTHERTHING",
		},

		sharedConfig: `[default]
aws_access_key_id = SOMETHING
aws_secret_access_key = ANOTHERTHING
`,
	}, {
		data: map[string]string{
			"credentials": `[default]
assume_role = this_role_for_ingress_operator
web_identity_token = /path/to/some/file
`,
		},

		sharedConfig: `[default]
assume_role = this_role_for_ingress_operator
web_identity_token = /path/to/some/file
`,
	}, {
		data: map[string]string{
			"aws_access_key_id":     "SOMETHING",
			"aws_secret_access_key": "ANOTHERTHING",
			"credentials": `[default]
aws_access_key_id = SOMETHING
aws_secret_access_key = ANOTHERTHING
`,
		},

		sharedConfig: `[default]
aws_access_key_id = SOMETHING
aws_secret_access_key = ANOTHERTHING
`,
	}, {
		data: map[string]string{
			"random_key": "random_value",
		},

		err: "invalid secret for aws credentials",
	}}
	for _, test := range cases {
		t.Run("", func(t *testing.T) {
			secret := &corev1.Secret{
				Data: map[string][]byte{},
			}
			for k, v := range test.data {
				secret.Data[k] = []byte(v)
			}

			fPath, err := SharedCredentialsFileFromSecret(secret)
			if fPath != "" {
				defer os.Remove(fPath)
			}

			if test.err == "" {
				assert.NoError(t, err)
				data, err := ioutil.ReadFile(fPath)
				assert.NoError(t, err)
				assert.Equal(t, string(data), test.sharedConfig)
			} else {
				assert.Regexp(t, test.err, err)
			}
		})
	}
}
