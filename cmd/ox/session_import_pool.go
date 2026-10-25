package main

import (
	"context"
	"sync"
)

// importPreparation transfers ownership of staging from a worker to the
// committer. The worker waits for done before preparing another session.
type importPreparation struct {
	index    int
	started  bool
	prepared *preparedImport
	skip     string
	err      error
	done     chan struct{}
}

// prepareImportPool keeps at most parallel sessions in preparation or waiting
// to publish. Results arrive in completion order; the candidates themselves
// stay in preview order for the final report. Even after cancellation each
// started job reports its result, so the committer can remove its staging.
func prepareImportPool(ctx context.Context, env *importEnv, selected []*importCandidate, parallel int) <-chan importPreparation {
	if parallel <= 0 {
		parallel = importDefaultParallel
	}
	parallel = min(parallel, len(selected))
	jobs := make(chan int)
	results := make(chan importPreparation)
	var workers sync.WaitGroup
	for range parallel {
		workers.Go(func() {
			for index := range jobs {
				if ctx.Err() != nil {
					return
				}
				results <- importPreparation{index: index, started: true}
				prepared, skip, err := prepareImport(ctx, env, selected[index])
				done := make(chan struct{})
				results <- importPreparation{index: index, prepared: prepared, skip: skip, err: err, done: done}
				<-done
			}
		})
	}
	go func() {
		defer close(jobs)
		for index := range selected {
			select {
			case <-ctx.Done():
				return
			case jobs <- index:
			}
		}
	}()
	go func() {
		workers.Wait()
		close(results)
	}()
	return results
}
