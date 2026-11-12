package app

import (
	"context"
	"fmt"
	"testing"
	"testing/synctest"
	"time"
)

// Фоновые проходы двух видов: по тику (первый — через интервал) и «подождал,
// сделал, выдержал паузу от конца прохода». Оба умирают вместе с ctx.
func TestBackgroundLoopsTiming(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		start := time.Now()
		var ticks, passes []time.Duration
		go everyTick(ctx, time.Hour, func() { ticks = append(ticks, time.Since(start)) })
		go afterThenEvery(ctx, time.Minute, time.Hour, func() {
			passes = append(passes, time.Since(start))
			time.Sleep(10 * time.Minute)
		})
		time.Sleep(3*time.Hour + 30*time.Minute)
		cancel()
		synctest.Wait()
		if got := fmt.Sprint(ticks); got != "[1h0m0s 2h0m0s 3h0m0s]" {
			t.Errorf("проходы по тику: %s", got)
		}
		if got := fmt.Sprint(passes); got != "[1m0s 1h11m0s 2h21m0s]" {
			t.Errorf("проходы с паузой от конца: %s", got)
		}
		time.Sleep(5 * time.Hour)
		synctest.Wait()
		if len(ticks) != 3 || len(passes) != 3 {
			t.Errorf("после отмены проходы продолжились: %d и %d", len(ticks), len(passes))
		}
	})
}
