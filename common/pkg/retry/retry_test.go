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

	// A delay below 10ns leaves no positive range for rand.N, which panics on a
	// non-positive argument. Such a delay is used exactly as given.
	for _, tt := range []struct {
		name  string
		delay time.Duration
	}{
		{"smallest representable delay", 1 * time.Nanosecond},
		{"delay below the jitter granularity", 5 * time.Nanosecond},
		{"largest delay whose jitter range truncates to zero", 9 * time.Nanosecond},
		{"smallest delay with a non-empty jitter range", 10 * time.Nanosecond},
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
				// rand.N(1) is always 0, so a 10ns delay is not jittered either.
				assert.Equal(t, 3*tt.delay, time.Since(start))
			})
		})
	}

	t.Run("delay is jittered when the range allows", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			const delay = time.Second
			attempts := 0
			start := time.Now()
			err := IfNecessary(context.Background(), alwaysFails(&attempts), &Options{
				MaxRetry: 3,
				Delay:    delay,
			})

			require.ErrorIs(t, err, syscall.ECONNREFUSED)
			assert.Equal(t, 4, attempts)
			elapsed := time.Since(start)
			assert.GreaterOrEqual(t, elapsed, 3*delay)
			assert.Less(t, elapsed, 3*delay+3*(delay/10), "jitter should add at most 10 %% per retry")
		})
	})

	t.Run("unset delay uses exponential backoff", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			attempts := 0
			start := time.Now()
			err := IfNecessary(context.Background(), alwaysFails(&attempts), &Options{MaxRetry: 3})

			require.ErrorIs(t, err, syscall.ECONNREFUSED)
			assert.Equal(t, 4, attempts)
			const base = 1*time.Second + 2*time.Second + 4*time.Second
			elapsed := time.Since(start)
			assert.GreaterOrEqual(t, elapsed, base)
			assert.Less(t, elapsed, base+base/10, "jitter should add at most 10 %% per retry")
		})
	})
}
