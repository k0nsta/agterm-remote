package bridge

import (
	"math/rand"
	"time"
)

const (
	initialBackoff = time.Second
	maxBackoff     = 30 * time.Second
)

// Next returns the next reconnect delay: roughly double the previous RETURNED
// delay, jittered by ±20%, so jitter compounds across attempts rather than
// decorating a clean 1/2/4/8 series. The doubling is capped at 30 seconds
// before jitter, so the value returned is at most 36 seconds. A connection
// that stayed healthy for more than 30 seconds resets the series to ~1 second.
// Jitter prevents several hosts reconnecting at the same instant.
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
	// result inside the ±20% range Next documents, including for a one-second
	// delay where integer rounding is visible.
	spread := float64(base) * 0.2
	value := float64(base) + (rand.Float64()*2-1)*spread
	if value < float64(time.Nanosecond) {
		value = float64(time.Nanosecond)
	}
	return time.Duration(value)
}
