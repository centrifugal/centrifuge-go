package centrifuge

import (
	"time"

	"github.com/jpillora/backoff"
)

type reconnectStrategy interface {
	timeBeforeNextAttempt(attempt int) time.Duration
}

type backoffReconnect struct {
	// Factor is the multiplying factor for each increment step.
	Factor float64
	// Jitter eases contention by randomizing backoff steps.
	Jitter bool
	// MinMilliseconds is a minimum value of reconnect interval.
	MinDelay time.Duration
	// MaxMilliseconds is a maximum value of reconnect interval.
	MaxDelay time.Duration
}

var defaultBackoffReconnect = &backoffReconnect{
	MinDelay: 200 * time.Millisecond,
	MaxDelay: 20 * time.Second,
	Factor:   2,
	Jitter:   true,
}

func (r *backoffReconnect) timeBeforeNextAttempt(attempt int) time.Duration {
	b := &backoff.Backoff{
		Min:    r.MinDelay,
		Max:    r.MaxDelay,
		Factor: r.Factor,
		Jitter: r.Jitter,
	}
	return b.ForAttempt(float64(attempt))
}
