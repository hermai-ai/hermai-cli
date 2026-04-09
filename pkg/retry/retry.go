package retry

import (
	"context"
	"math"
	"math/rand/v2"
	"time"
)

// Config controls retry behavior.
type Config struct {
	MaxAttempts int           // total attempts including the first (default 3)
	BaseDelay   time.Duration // initial delay between retries (default 500ms)
	MaxDelay    time.Duration // cap on exponential backoff (default 5s)
}

// Default returns a production-ready retry config.
func Default() Config {
	return Config{
		MaxAttempts: 3,
		BaseDelay:   500 * time.Millisecond,
		MaxDelay:    5 * time.Second,
	}
}

// Do executes fn, retrying on transient errors. The shouldRetry function
// determines whether a given error is retryable. Returns the first
// successful result or the last error after all attempts are exhausted.
func Do[T any](ctx context.Context, cfg Config, fn func() (T, error), shouldRetry func(error) bool) (T, error) {
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 1
	}

	var lastErr error
	var zero T

	for attempt := 0; attempt < cfg.MaxAttempts; attempt++ {
		result, err := fn()
		if err == nil {
			return result, nil
		}

		lastErr = err

		if !shouldRetry(err) {
			return zero, err
		}

		// Don't sleep after the last attempt
		if attempt < cfg.MaxAttempts-1 {
			delay := backoffDelay(cfg.BaseDelay, cfg.MaxDelay, attempt)
			select {
			case <-ctx.Done():
				return zero, ctx.Err()
			case <-time.After(delay):
			}
		}
	}

	return zero, lastErr
}

// backoffDelay calculates exponential backoff with jitter.
// Jitter prevents thundering herd when multiple requests retry simultaneously.
func backoffDelay(base, max time.Duration, attempt int) time.Duration {
	delay := time.Duration(float64(base) * math.Pow(2, float64(attempt)))
	if delay > max {
		delay = max
	}
	// Add up to 25% jitter
	jitter := time.Duration(rand.Int64N(int64(delay/4) + 1))
	return delay + jitter
}
