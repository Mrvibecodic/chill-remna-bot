package app

import (
	"context"
	"time"
)

// everyTick зовёт work на каждом тике, пока жив ctx. Первый проход — через
// interval после старта; тик, пришедшийся на идущий проход, не копится.
func everyTick(ctx context.Context, interval time.Duration, work func()) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			work()
		}
	}
}

// afterThenEvery ждёт first, затем зовёт work снова и снова; pause отсчитывается
// от конца прохода, поэтому долгий проход следующий не догоняет.
func afterThenEvery(ctx context.Context, first, pause time.Duration, work func()) {
	wait := first
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		work()
		wait = pause
	}
}
