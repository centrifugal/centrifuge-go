package centrifuge_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/centrifugal/centrifuge-go"
)

func TestErrors(t *testing.T) {
	cases := []struct {
		name      string
		rootError error
		factory   func(err error) error
	}{
		{
			name:      "SubscriptionSubscribeError",
			rootError: centrifuge.ErrTimeout,
			factory: func(err error) error {
				return centrifuge.SubscriptionSubscribeError{Err: err}
			},
		},
		{
			name:      "SubscriptionRefreshError",
			rootError: centrifuge.ErrUnauthorized,
			factory: func(err error) error {
				return centrifuge.SubscriptionRefreshError{Err: err}
			},
		},
		{
			name:      "ConfigurationError",
			rootError: centrifuge.ErrClientClosed,
			factory: func(err error) error {
				return centrifuge.ConfigurationError{Err: err}
			},
		},
		{
			name:      "RefreshError",
			rootError: centrifuge.ErrClientClosed,
			factory: func(err error) error {
				return centrifuge.RefreshError{Err: err}
			},
		},
		{
			name:      "ConnectError",
			rootError: centrifuge.ErrClientClosed,
			factory: func(err error) error {
				return centrifuge.ConnectError{Err: err}
			},
		},
		{
			name:      "TransportError",
			rootError: centrifuge.ErrClientClosed,
			factory: func(err error) error {
				return centrifuge.TransportError{Err: err}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.factory(c.rootError)
			parts := strings.Split(err.Error(), ": ")
			if parts[1] != c.rootError.Error() {
				t.Errorf("unexpected error string: %v", err)
			}

			if !errors.Is(err, c.rootError) {
				t.Errorf("expected root error to be wrapped")
			}
		})
	}
}
