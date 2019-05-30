package client

import (
	"errors"
	"strings"
)

type Zone struct {
	SubscriptionID string
	ResourceGroup  string
	Name           string
}

func ParseZone(id string) (*Zone, error) {
	s := strings.Split(id, "/")
	if len(s) < 9 {
		return nil, errors.New("invalid azure dns zone id")
	}
	return &Zone{SubscriptionID: s[2], ResourceGroup: s[4], Name: s[8]}, nil
}
