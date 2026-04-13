package twinapi

import (
	"net/http"
	"time"
)

// apiMux returns the HTTP handler for the API port (what ox talks to).
func (tw *Twin) apiMux() http.Handler {
	mux := http.NewServeMux()

	// device flow
	mux.HandleFunc("POST /api/auth/device/code", tw.handleDeviceCode)
	mux.HandleFunc("POST /api/v1/device/token", tw.handleDeviceToken)

	// JWT exchange
	mux.HandleFunc("GET /api/v1/cli/auth/token", tw.handleJWTExchange)

	// OAuth2
	mux.HandleFunc("GET /oauth2/userinfo", tw.handleUserInfo)
	mux.HandleFunc("POST /oauth2/token", tw.handleTokenRefresh)
	mux.HandleFunc("POST /oauth2/revoke", tw.handleRevoke)

	return tw.faultMiddleware(tw.recordMiddleware(mux))
}

// adminMux returns the HTTP handler for the admin port (test control).
func (tw *Twin) adminMux() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /reset", tw.handleAdminReset)
	mux.HandleFunc("POST /users", tw.handleAdminCreateUser)
	mux.HandleFunc("POST /users/{id}/sessions", tw.handleAdminCreateSession)
	mux.HandleFunc("POST /sessions/orphaned", tw.handleAdminCreateOrphanedSession)
	mux.HandleFunc("POST /fault", tw.handleAdminInjectFault)
	mux.HandleFunc("DELETE /fault", tw.handleAdminClearFaults)
	mux.HandleFunc("GET /calls", tw.handleAdminGetCalls)
	mux.HandleFunc("POST /clock/advance", tw.handleAdminAdvanceClock)

	return mux
}

// recordMiddleware wraps a handler to record each call to the store.
func (tw *Twin) recordMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rec, r)
		tw.store.recordCall(r.Method, r.URL.Path, rec.statusCode)
	})
}

// faultMiddleware intercepts requests and returns configured faults.
func (tw *Twin) faultMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var (
			shouldFault bool
			statusCode  int
			body        string
			latency     time.Duration
		)

		tw.store.mu.Lock()
		fault, ok := tw.store.faults[r.URL.Path]
		if ok {
			fault.count++
			if fault.count > fault.After {
				shouldFault = true
				statusCode = fault.StatusCode
				body = fault.Body
				latency = fault.Latency
			}
		}
		tw.store.mu.Unlock()

		if shouldFault {
			if latency > 0 {
				time.Sleep(latency)
			}
			tw.store.recordCall(r.Method, r.URL.Path, statusCode)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(statusCode)
			if body != "" {
				_, _ = w.Write([]byte(body))
			}
			return
		}
		next.ServeHTTP(w, r)
	})
}

// statusRecorder captures the status code written by the handler.
type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}
