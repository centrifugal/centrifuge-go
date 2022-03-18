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
	MinMilliseconds int
	// MaxMilliseconds is a maximum value of reconnect interval.
	MaxMilliseconds int
}

var defaultBackoffReconnect = &backoffReconnect{
	MinMilliseconds: 200,
	MaxMilliseconds: 20 * 1000,
	Factor:          2,
	Jitter:          true,
}

func (r *backoffReconnect) timeBeforeNextAttempt(attempt int) time.Duration {
	b := &backoff.Backoff{
		Min:    time.Duration(r.MinMilliseconds) * time.Millisecond,
		Max:    time.Duration(r.MaxMilliseconds) * time.Millisecond,
		Factor: r.Factor,
		Jitter: r.Jitter,
	}
	return b.ForAttempt(float64(attempt))
}
