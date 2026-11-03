package retry

import (
	"context"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIfNecessary(t *testing.T) {
	// alwaysFails counts its invocations and always returns a retryable error.
	alwaysFails := func(attempts *int) func() error {
		return func() error {
			*attempts++
			return syscall.ECONNREFUSED
		}
	}

	t.Run("negative delay is rejected", func(t *testing.T) {
		attempts := 0
		err := IfNecessary(context.Background(), alwaysFails(&attempts), &Options{
			MaxRetry: 3,
			Delay:    -1 * time.Second,
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid retry delay")
		assert.Zero(t, attempts, "the operation should not run at all")
	})

	for _, tt := range []struct {
		name string
		// delay is Options.Delay; zero leaves it unset, which selects
		// exponential backoff.
		delay time.Duration
		// minTotalTime and maxTotalTime bound, inclusively, the total time
		// spent waiting between attempts.
		minTotalTime, maxTotalTime time.Duration
	}{
		// A delay below 10ns leaves no positive range for rand.N, which panics
		// on a non-positive argument, so such a delay is used exactly as given.
		{"smallest representable delay", 1 * time.Nanosecond, 3 * time.Nanosecond, 3 * time.Nanosecond},
		{"delay below the jitter granularity", 5 * time.Nanosecond, 15 * time.Nanosecond, 15 * time.Nanosecond},
		{"largest delay whose jitter range truncates to zero", 9 * time.Nanosecond, 27 * time.Nanosecond, 27 * time.Nanosecond},
		// rand.N(1) is always 0, so a 10ns delay is not jittered either.
		{"smallest delay with a non-empty jitter range", 10 * time.Nanosecond, 30 * time.Nanosecond, 30 * time.Nanosecond},
		// Three retries of a second each, with up to 10 % jitter added to every one.
		{"delay is jittered when the range allows", time.Second, 3 * time.Second, 3*time.Second + 300*time.Millisecond},
		// Exponential backoff of 1s + 2s + 4s, again with up to 10 % jitter each.
		{"unset delay uses exponential backoff", 0, 7 * time.Second, 7*time.Second + 700*time.Millisecond},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				attempts := 0
				start := time.Now()
				err := IfNecessary(context.Background(), alwaysFails(&attempts), &Options{
					MaxRetry: 3,
					Delay:    tt.delay,
				})

				require.ErrorIs(t, err, syscall.ECONNREFUSED)
				assert.Equal(t, 4, attempts, "the operation should run once and then be retried MaxRetry times")
				// Within synctest.Test the clock is virtual and advances only
				// while every goroutine is durably blocked, so elapsed is
				// exactly the time the retries waited for; goroutine scheduling
				// cannot inflate it.
				elapsed := time.Since(start)
				assert.GreaterOrEqual(t, elapsed, tt.minTotalTime)
				assert.LessOrEqual(t, elapsed, tt.maxTotalTime)
			})
		})
	}
}
