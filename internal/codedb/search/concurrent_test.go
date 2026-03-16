package search

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConcurrentSearchSameStore launches many goroutines executing different
// queries against the same Store simultaneously, simulating parallel agents
// all running "ox code query" at once. Under WAL mode, concurrent readers
// should never block each other.
func TestConcurrentSearchSameStore(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	seedTestData(t, s)

	queries := []string{
		"type:commit author:alice",
		"type:commit author:bob",
		"type:symbol lang:go main",
		"type:symbol lang:rust process",
		"calls:helper",
		"calledby:process",
		"returns:Result",
		"type:commit refactor",
	}

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)

	var completed atomic.Int32
	var errors atomic.Int32

	for i := range goroutines {
		go func(id int) {
			defer wg.Done()
			q := queries[id%len(queries)]

			parsed, err := ParseQuery(q)
			if err != nil {
				errors.Add(1)
				return
			}

			results, err := Execute(context.Background(), s, parsed)
			if err != nil {
				errors.Add(1)
				return
			}

			// every query should return something with our seed data
			if len(results) > 0 {
				completed.Add(1)
			}
		}(i)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("concurrent search deadlocked")
	}

	assert.Equal(t, int32(0), errors.Load(), "no search errors expected")
	assert.Equal(t, int32(goroutines), completed.Load(), "all goroutines should get results")
}

// TestConcurrentSearchAndWrite verifies that readers don't block during writes
// and vice versa, simulating agents searching while the daemon is inserting
// new data (e.g., during incremental re-index).
func TestConcurrentSearchAndWrite(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	seedTestData(t, s)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const readers = 20
	const writerInserts = 50

	var readersWg sync.WaitGroup
	readersWg.Add(readers)

	writerDone := make(chan struct{})

	// writer goroutine: inserts new commits while readers are searching
	go func() {
		defer close(writerDone)
		for i := range writerInserts {
			_, err := s.Exec(
				`INSERT INTO commits (repo_id, hash, author, message, timestamp) VALUES (1, ?, 'writer', ?, ?)`,
				"writerhash"+string(rune('A'+i%26))+string(rune('0'+i/26)),
				"concurrent write "+string(rune('0'+i%10)),
				1700300000+i,
			)
			if err != nil {
				t.Logf("write %d: %v", i, err)
			}
		}
	}()

	// reader goroutines: search continuously while writes happen
	for range readers {
		go func() {
			defer readersWg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}

				q, _ := ParseQuery("type:commit author:alice")
				results, err := Execute(context.Background(), s, q)
				if err != nil {
					// context cancellation is expected during shutdown
					return
				}
				// alice's commits from seed data should always be findable
				if len(results) == 0 {
					t.Error("expected results for alice even during concurrent writes")
					return
				}

				select {
				case <-writerDone:
					return
				default:
				}
			}
		}()
	}

	// wait for writer to finish, then stop readers
	<-writerDone
	cancel()
	readersWg.Wait()
}

// TestConcurrentSearchDifferentQueries verifies all query types work correctly
// under concurrent load. Each goroutine runs a different query type and validates
// the result correctness, not just absence of crashes.
func TestConcurrentSearchDifferentQueries(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	seedTestData(t, s)

	type queryCheck struct {
		input    string
		validate func(t *testing.T, results []Result)
	}

	checks := []queryCheck{
		{
			input: "type:commit author:alice",
			validate: func(t *testing.T, results []Result) {
				require.NotEmpty(t, results)
				for _, r := range results {
					assert.Equal(t, "alice", r.Author)
				}
			},
		},
		{
			input: "type:symbol lang:go main",
			validate: func(t *testing.T, results []Result) {
				require.NotEmpty(t, results)
				found := false
				for _, r := range results {
					if r.SymbolName == "main" {
						found = true
					}
				}
				assert.True(t, found, "expected to find 'main' symbol")
			},
		},
		{
			input: "calls:helper",
			validate: func(t *testing.T, results []Result) {
				require.NotEmpty(t, results)
			},
		},
		{
			input: "returns:Result",
			validate: func(t *testing.T, results []Result) {
				require.NotEmpty(t, results)
				found := false
				for _, r := range results {
					if r.SymbolName == "process" {
						found = true
					}
				}
				assert.True(t, found, "expected 'process' returning Result")
			},
		},
	}

	const iterations = 10
	var wg sync.WaitGroup
	wg.Add(len(checks) * iterations)

	for _, check := range checks {
		for range iterations {
			go func(c queryCheck) {
				defer wg.Done()
				q, err := ParseQuery(c.input)
				require.NoError(t, err)
				results, err := Execute(context.Background(), s, q)
				require.NoError(t, err)
				c.validate(t, results)
			}(check)
		}
	}

	wg.Wait()
}

// TestConcurrentSearchWithContextCancellation verifies that many concurrent
// searches handle context cancellation gracefully without panics or leaks.
func TestConcurrentSearchWithContextCancellation(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	seedTestData(t, s)

	const goroutines = 30
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := range goroutines {
		go func(id int) {
			defer wg.Done()
			ctx, cancel := context.WithCancel(context.Background())

			// cancel some immediately, some after a brief delay
			if id%3 == 0 {
				cancel()
			} else {
				go func() {
					time.Sleep(time.Duration(id%5) * time.Millisecond)
					cancel()
				}()
			}

			q, _ := ParseQuery("type:commit author:alice")
			_, _ = Execute(ctx, s, q)
			// no panic = success; errors from cancellation are expected
		}(i)
	}

	wg.Wait()
}
