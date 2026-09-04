package bridge

import (
	"math/rand"
	"time"
)

const (
	initialBackoff = time.Second
	maxBackoff     = 30 * time.Second
)

// Next returns the next reconnect delay. The unjittered delay doubles on each
// failed attempt, is capped at 30 seconds, and is reset after a connection
// remains healthy for more than 30 seconds. Jitter prevents several hosts
// reconnecting at the same instant.
func Next(previous, connectedFor time.Duration) time.Duration {
	base := initialBackoff
	if connectedFor > maxBackoff {
		return jitter(base)
	}
	if previous > 0 {
		base = previous * 2
		if base > maxBackoff {
			base = maxBackoff
		}
	}
	return jitter(base)
}

func jitter(base time.Duration) time.Duration {
	// rand.Float64 is concurrency-safe for the package-level source. Keep the
	// result inside the documented +/-20% range, including for a one-second
	// delay where integer rounding is visible.
	spread := float64(base) * 0.2
	value := float64(base) + (rand.Float64()*2-1)*spread
	if value < float64(time.Nanosecond) {
		value = float64(time.Nanosecond)
	}
	return time.Duration(value)
}
