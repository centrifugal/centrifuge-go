package centrifuge

import (
	"time"

	"github.com/jpillora/backoff"
)

type reconnectStrategy interface {
	timeBeforeNextAttempt(attempt int) (time.Duration, error)
}

type backoffReconnect struct {
	// NumReconnect is maximum number of reconnect attempts, 0 means reconnect forever.
	NumReconnect int
	// Factor is the multiplying factor for each increment step.
	Factor float64
	// Jitter eases contention by randomizing backoff steps.
	Jitter bool
	// MinMilliseconds is a minimum value of the reconnect interval.
	MinMilliseconds int
	// MaxMilliseconds is a maximum value of the reconnect interval.
	MaxMilliseconds int
}

var defaultBackoffReconnect = &backoffReconnect{
	NumReconnect:    0,
	MinMilliseconds: 100,
	MaxMilliseconds: 20 * 1000,
	Factor:          4,
	Jitter:          true,
}

func (r *backoffReconnect) timeBeforeNextAttempt(attempt int) (time.Duration, error) {
	b := &backoff.Backoff{
		Min:    time.Duration(r.MinMilliseconds) * time.Millisecond,
		Max:    time.Duration(r.MaxMilliseconds) * time.Millisecond,
		Factor: r.Factor,
		Jitter: r.Jitter,
	}
	if r.NumReconnect > 0 && attempt >= r.NumReconnect {
		return 0, ErrReconnectFailed
	}
	return b.ForAttempt(float64(attempt)), nil
}
