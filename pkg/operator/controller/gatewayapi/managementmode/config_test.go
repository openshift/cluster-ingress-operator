package managementmode

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateManagementModeConfig(t *testing.T) {
	assert.NoError(t, ValidateManagementModeConfig(false, nil))
	assert.NoError(t, ValidateManagementModeConfig(true, NewStore(State{})))
	assert.Error(t, ValidateManagementModeConfig(true, nil))
}
