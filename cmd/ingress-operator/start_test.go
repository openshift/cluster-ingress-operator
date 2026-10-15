package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDeprecatedHAProxyVersionDefault(t *testing.T) {
	t.Parallel()
	versions, err := NewStartCommand().Flags().GetStringSlice("deprecated-haproxy-version")
	require.NoError(t, err)
	require.Equal(t, []string{"2.8"}, versions)
}
