package util

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestClamp(t *testing.T) {
	cases := []struct {
		val      int64
		min      int64
		max      int64
		excepted int64
	}{
		{30, 5, 60, 30},
		{30, 60, 300, 60},
		{300, 30, 60, 60},
		{30, 30, 60, 30},
		{60, 30, 60, 60},
	}

	for _, c := range cases {
		assert.Equal(t, c.excepted, Clamp(c.val, c.min, c.max))
	}
}
