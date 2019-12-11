package retryableerror

import (
	"errors"
	"testing"
	"time"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"
)

func TestRetryableError(t *testing.T) {
	tests := []struct {
		name            string
		errors          []error
		expectRetryable bool
		expectAggregate bool
		expectAfter     time.Duration
	}{
		{
			name: "empty list",
		},
		{
			name:   "nil error",
			errors: []error{nil},
		},
		{
			name:            "non-retryable errors",
			errors:          []error{errors.New("foo"), errors.New("bar")},
			expectAggregate: true,
		},
		{
			name: "mix of retryable and non-retryable errors",
			errors: []error{
				errors.New("foo"),
				errors.New("bar"),
				New(errors.New("baz"), time.Second*15),
				New(errors.New("quux"), time.Minute),
			},
			expectAggregate: true,
		},
		{
			name: "only retryable errors",
			errors: []error{
				New(errors.New("baz"), time.Second*15),
				New(errors.New("quux"), time.Minute),
				nil,
			},
			expectRetryable: true,
			expectAfter:     time.Second * 15,
		},
	}
	for _, test := range tests {
		err := NewMaybeRetryableAggregate(test.errors)
		if retryable, gotRetryable := err.(Error); gotRetryable != test.expectRetryable {
			t.Errorf("%q: expected retryable %T, got %T: %v", test.name, test.expectRetryable, gotRetryable, err)
		} else if gotRetryable && retryable.After() != test.expectAfter {
			t.Errorf("%q: expected after %v, got %v: %v", test.name, test.expectAfter, retryable.After(), err)
		}
		if _, gotAggregate := err.(utilerrors.Aggregate); gotAggregate != test.expectAggregate {
			t.Errorf("%q: expected aggregate %T, got %T: %v", test.name, test.expectAggregate, gotAggregate, err)
		}
	}
}
