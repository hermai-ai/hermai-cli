package retry

import (
	"context"
	"errors"
	"testing"
	"time"
)

var errTransient = errors.New("transient")
var errPermanent = errors.New("permanent")

func isTransient(err error) bool {
	return errors.Is(err, errTransient)
}

func TestDo_SuccessOnFirstAttempt(t *testing.T) {
	calls := 0
	result, err := Do(context.Background(), Default(), func() (string, error) {
		calls++
		return "ok", nil
	}, isTransient)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "ok" {
		t.Errorf("expected 'ok', got %q", result)
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
}

func TestDo_RetriesOnTransientError(t *testing.T) {
	calls := 0
	cfg := Config{MaxAttempts: 3, BaseDelay: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond}

	result, err := Do(context.Background(), cfg, func() (string, error) {
		calls++
		if calls < 3 {
			return "", errTransient
		}
		return "recovered", nil
	}, isTransient)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "recovered" {
		t.Errorf("expected 'recovered', got %q", result)
	}
	if calls != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
}

func TestDo_NoRetryOnPermanentError(t *testing.T) {
	calls := 0

	_, err := Do(context.Background(), Default(), func() (string, error) {
		calls++
		return "", errPermanent
	}, isTransient)

	if !errors.Is(err, errPermanent) {
		t.Errorf("expected permanent error, got: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call (no retry), got %d", calls)
	}
}

func TestDo_ExhaustsAllAttempts(t *testing.T) {
	calls := 0
	cfg := Config{MaxAttempts: 3, BaseDelay: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond}

	_, err := Do(context.Background(), cfg, func() (string, error) {
		calls++
		return "", errTransient
	}, isTransient)

	if !errors.Is(err, errTransient) {
		t.Errorf("expected transient error, got: %v", err)
	}
	if calls != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
}

func TestDo_RespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	calls := 0
	cfg := Config{MaxAttempts: 5, BaseDelay: 1 * time.Second, MaxDelay: 5 * time.Second}

	_, err := Do(ctx, cfg, func() (string, error) {
		calls++
		return "", errTransient
	}, isTransient)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call before cancel, got %d", calls)
	}
}

func TestBackoffDelay(t *testing.T) {
	base := 100 * time.Millisecond
	max := 2 * time.Second

	// With jitter, delay is base * 2^attempt + up to 25% jitter
	d0 := backoffDelay(base, max, 0)
	if d0 < 100*time.Millisecond || d0 > 125*time.Millisecond {
		t.Errorf("attempt 0: expected 100-125ms, got %v", d0)
	}

	d1 := backoffDelay(base, max, 1)
	if d1 < 200*time.Millisecond || d1 > 250*time.Millisecond {
		t.Errorf("attempt 1: expected 200-250ms, got %v", d1)
	}

	d5 := backoffDelay(base, max, 5)
	if d5 < max || d5 > max+max/4 {
		t.Errorf("attempt 5: expected %v to %v, got %v", max, max+max/4, d5)
	}
}
