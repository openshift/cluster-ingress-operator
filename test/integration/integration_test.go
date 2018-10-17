// +build integration

package integration

import (
	"flag"
	"fmt"
	"math/rand"
	"os"
	"testing"
	"time"
)

var (
	clusterName = flag.String("cluster-name", "", "cluster name")
)

func TestMain(m *testing.M) {
	flag.Parse()

	if clusterName == nil || len(*clusterName) == 0 {
		fmt.Println("cluster name is required")
		os.Exit(1)
	}

	os.Exit(m.Run())
}

func TestIntegration(t *testing.T) {
	tc := TestConfig{
		ClusterName: *clusterName,
	}

	// Execute subtests
	t.Run("TestDefaultIngress", func(t *testing.T) { testDefaultIngress(t, tc) })
}

type TestConfig struct {
	ClusterName string
}

func init() {
	rand.Seed(time.Now().UnixNano())
}

var letters = []rune("abcdefghijklmnopqrstuvwxyz")

func RandSeq(n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}
