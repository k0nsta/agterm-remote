package bridge

import (
	"testing"
	"time"
)

func TestNextDoublesWithJitterAndCaps(t *testing.T) {
	t.Helper()
	tests := []struct {
		name      string
		previous  time.Duration
		connected time.Duration
		base      time.Duration
	}{
		{name: "initial", base: time.Second},
		{name: "doubles", previous: time.Second, base: 2 * time.Second},
		{name: "caps", previous: 20 * time.Second, base: maxBackoff},
		{name: "resets after healthy connection", previous: 20 * time.Second, connected: maxBackoff + time.Nanosecond, base: time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Helper()
			for range 100 {
				got := Next(test.previous, test.connected)
				lower := time.Duration(float64(test.base) * 0.8)
				upper := time.Duration(float64(test.base) * 1.2)
				if got < lower || got > upper {
					t.Fatalf("Next(%s, %s) = %s, want between %s and %s", test.previous, test.connected, got, lower, upper)
				}
			}
		})
	}
}

func TestNextUsesPreviousDelayForRepeatedFailures(t *testing.T) {
	t.Helper()
	tests := []struct {
		previous time.Duration
		base     time.Duration
	}{
		{base: time.Second},
		{previous: time.Second, base: 2 * time.Second},
		{previous: 2 * time.Second, base: 4 * time.Second},
		{previous: 4 * time.Second, base: 8 * time.Second},
		{previous: 8 * time.Second, base: 16 * time.Second},
		{previous: 16 * time.Second, base: maxBackoff},
	}
	for _, test := range tests {
		got := Next(test.previous, 0)
		lower := time.Duration(float64(test.base) * 0.8)
		upper := time.Duration(float64(test.base) * 1.2)
		if got < lower || got > upper {
			t.Fatalf("Next(%s, 0) = %s, want between %s and %s", test.previous, got, lower, upper)
		}
	}
}
