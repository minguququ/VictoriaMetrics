package kuma

import (
	"context"
	"time"
)

func initAndRepeatWithInterval(interval time.Duration, action func(ctx context.Context)) (context.CancelFunc, chan struct{}) {
	ctx, cancel := context.WithCancel(context.Background())

	initCh := make(chan struct{})

	go func() {
		action(ctx)
		close(initCh)

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				action(ctx)
			case <-ctx.Done():
				return
			}
		}
	}()

	return cancel, initCh
}
