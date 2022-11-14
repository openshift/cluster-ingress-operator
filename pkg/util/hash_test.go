package k8s

import (
	"testing"
)

func Test_Hash(t *testing.T) {
	if Hash("foo") != Hash("foo") {
		t.Errorf("Hash function result should be reproducible")
	}

	if Hash("foo") == Hash("bar") {
		t.Errorf("Hash function result should be unique if Namespace and Name do not match")
	}
}
