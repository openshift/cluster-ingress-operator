package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

var (
	DefaultProject = "defaultProject"
)

func TestParseZone(t *testing.T) {
	cases := []struct {
		name         string
		providedZone string
		expectedID   string
		expectedZone string
		errStr       string
	}{{
		name:         "Valid Zone ID With Default Project",
		providedZone: "validZone",
		expectedID:   "defaultProject",
		expectedZone: "validZone",
	}, {
		name:         "Valid Embedded Zone and Project",
		providedZone: "projects/validProject/managedZones/validZone",
		expectedZone: "validZone",
		expectedID:   "validProject",
	}, {
		name:         "Invalid Too Many Values",
		providedZone: "projects/validProject/managedZones/validZone/extras",
		errStr:       "invalid managedZone: projects/validProject/managedZones/validZone/extras",
	}, {
		name:         "Invalid Too Few Values",
		providedZone: "projects/validProject/validZone",
		errStr:       "invalid managedZone: projects/validProject/validZone",
	}, {
		name:         "Invalid Zone String Projects",
		providedZone: "project/validProject/managedZones/validZone",
		errStr:       "invalid managedZone: project/validProject/managedZones/validZone",
	}, {
		name:         "Invalid Zone String Zone",
		providedZone: "projects/validProject/managedZone/validZone",
		errStr:       "invalid managedZone: projects/validProject/managedZone/validZone",
	},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			project, zoneID, err := ParseZone("defaultProject", tc.providedZone)
			if err != nil {
				assert.Equal(t, tc.errStr, err.Error())
			} else {
				assert.Equal(t, tc.expectedID, project)
				assert.Equal(t, tc.expectedZone, zoneID)
			}
		})
	}
}
