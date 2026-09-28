package service

import (
	"context"
	"errors"
	"os"
	"strconv"
	"sync"
)

const defaultDegradationConcurrency = 8

func degradationConcurrencyFromEnv() (int, error) {
	value := defaultDegradationConcurrency
	if raw, ok := os.LookupEnv("DEGRADATION_CHECK_CONCURRENCY"); ok {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 64 {
			return 0, errors.New("invalid DEGRADATION_CHECK_CONCURRENCY")
		}
		value = parsed
	}
	return value, nil
}

// Only HTTP runs concurrently. Callers persist samples, output JSONL, advance
// cursors and aggregate snapshots on one goroutine, avoiding SQLite write races
// and preserving deterministic output independently of response completion order.
func probeDegradationTargets(ctx context.Context, targets []DegradationTarget, concurrency int, enabled func() bool) ([]DegradationResult, error) {
	if concurrency == 0 {
		concurrency = defaultDegradationConcurrency
	}
	if concurrency < 1 || concurrency > 64 {
		return nil, errors.New("invalid degradation concurrency")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make([]DegradationResult, len(targets))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range min(concurrency, len(targets)) {
		workers.Go(func() {
			for index := range jobs {
				if enabled != nil && !enabled() {
					cancel()
				}
				if ctx.Err() != nil {
					return
				}
				results[index] = ProbeDegradation(ctx, targets[index])
			}
		})
	}
dispatch:
	for index := range targets {
		select {
		case <-ctx.Done():
			break dispatch
		case jobs <- index:
		}
	}
	close(jobs)
	workers.Wait()
	return results, ctx.Err()
}
