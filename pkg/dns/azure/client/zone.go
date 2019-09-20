package client

import (
	"errors"
	"fmt"
	"strings"
)

type Zone struct {
	SubscriptionID string
	ResourceGroup  string
	Provider       string
	Name           string
}

func ParseZone(id string) (*Zone, error) {
	s := strings.Split(id, "/")
	if len(s) < 9 {
		return nil, errors.New("invalid azure dns zone id")
	}
	return &Zone{SubscriptionID: s[2], ResourceGroup: s[4], Provider: fmt.Sprintf("%s/%s", s[6], s[7]), Name: s[8]}, nil
}
