package client

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTelemetryOptions(t *testing.T) {
	opts := telemetryOptions()

	assert.Equal(t, "cluster-ingress-operator", opts.ApplicationID, "unexpected ApplicationID in telemetry options")
	assert.LessOrEqual(t, len(opts.ApplicationID), 24, "ApplicationID exceeds the Azure SDK 24-character limit")
	assert.NotContains(t, opts.ApplicationID, " ", "ApplicationID must not contain spaces; the Azure SDK replaces them with '/'")
}
