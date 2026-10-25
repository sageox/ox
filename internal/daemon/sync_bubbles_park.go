package daemon

import (
	"sync"
	"time"
)

// kb scope park backoff. A scope whose list call fails with an auth error is
// not retried every sync cycle (15s); it is parked and retried on an
// exponential schedule so a persistently rejected scope cannot flood the log
// or the API.
const (
	kbScopeParkInitial = time.Minute
	kbScopeParkMax     = time.Hour
)

// kbScopeParkState is the backoff record for one parked scope.
type kbScopeParkState struct {
	failures int
	retryAt  time.Time
}

// kbScopeParks tracks parked kb scopes keyed by scope id. The zero value is
// ready to use; a nil clock means time.Now (tests inject one).
type kbScopeParks struct {
	mu     sync.Mutex
	now    func() time.Time
	scopes map[string]*kbScopeParkState
}

func (p *kbScopeParks) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

// parked reports whether the scope is still inside its backoff window.
func (p *kbScopeParks) parked(scopeID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	st, ok := p.scopes[scopeID]
	return ok && p.clock().Before(st.retryAt)
}

// fail records a failed attempt and returns the new failure count and the
// delay until the next attempt: 1m, 2m, 4m, ... capped at 1h.
func (p *kbScopeParks) fail(scopeID string) (attempts int, delay time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.scopes == nil {
		p.scopes = make(map[string]*kbScopeParkState)
	}
	st := p.scopes[scopeID]
	if st == nil {
		st = &kbScopeParkState{}
		p.scopes[scopeID] = st
	}
	st.failures++
	delay = kbScopeParkInitial
	for i := 1; i < st.failures && delay < kbScopeParkMax; i++ {
		delay *= 2
	}
	if delay > kbScopeParkMax {
		delay = kbScopeParkMax
	}
	st.retryAt = p.clock().Add(delay)
	return st.failures, delay
}

// succeed clears any park for the scope and reports whether one existed.
func (p *kbScopeParks) succeed(scopeID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.scopes[scopeID]
	delete(p.scopes, scopeID)
	return ok
}

// clear lifts every park (manual sync).
func (p *kbScopeParks) clear() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.scopes = nil
}

// clearKBScopeParks lifts every parked kb scope so the next cycle retries
// them. Called from manual sync paths.
func (s *SyncScheduler) clearKBScopeParks() { s.kbScopeParks.clear() }
