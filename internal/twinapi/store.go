package twinapi

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

type store struct {
	mu        sync.RWMutex
	jwtSecret []byte // per-instance, 32 bytes from crypto/rand
	clock     time.Time

	users       map[string]*User       // user_id -> User
	sessions    map[string]*Session    // opaque_token -> Session
	teams       map[string]*Team       // team_id -> Team
	repos       map[string]*Repo       // repo_id -> Repo
	deviceCodes map[string]*DeviceCode // device_code -> DeviceCode

	calls  []CallRecord
	faults map[string]*EndpointFault // path pattern -> fault config
}

// User represents an authenticated user in the twin.
type User struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
	Tier  string `json:"tier"`
}

// Session represents an active session bound to a user.
type Session struct {
	Token        string    `json:"token"`
	UserID       string    `json:"user_id"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// Team represents a SageOx team.
type Team struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// Repo represents a SageOx-tracked repository.
type Repo struct {
	ID          string `json:"id"`
	TeamID      string `json:"team_id"`
	Fingerprint string `json:"fingerprint"`
}

// DeviceCode tracks a pending device authorization flow.
type DeviceCode struct {
	DeviceCode string
	UserCode   string
	Approved   bool
	UserID     string // set when approved
	ExpiresAt  time.Time
}

// CallRecord captures an API call for test assertions.
type CallRecord struct {
	Method     string    `json:"method"`
	Path       string    `json:"path"`
	StatusCode int       `json:"status_code"`
	Timestamp  time.Time `json:"timestamp"`
}

// EndpointFault configures synthetic failures for a path.
type EndpointFault struct {
	StatusCode int           `json:"status_code"`
	Body       string        `json:"body"`
	Latency    time.Duration `json:"latency"`
	After      int           `json:"after"` // only fault after N successful calls
	count      int
}

func newStore() *store {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		panic(fmt.Sprintf("twinapi: failed to generate jwt secret: %v", err))
	}

	return &store{
		jwtSecret:   secret,
		clock:       time.Now(),
		users:       make(map[string]*User),
		sessions:    make(map[string]*Session),
		teams:       make(map[string]*Team),
		repos:       make(map[string]*Repo),
		deviceCodes: make(map[string]*DeviceCode),
		calls:       nil,
		faults:      make(map[string]*EndpointFault),
	}
}

func (s *store) now() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.clock
}

func (s *store) advanceClock(d time.Duration) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clock = s.clock.Add(d)
	return s.clock
}

func (s *store) recordCall(method, path string, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, CallRecord{
		Method:     method,
		Path:       path,
		StatusCode: status,
		Timestamp:  s.clock,
	})
}

// generateToken returns a 32-char hex string from crypto/rand.
func (s *store) generateToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("twinapi: failed to generate token: %v", err))
	}
	return hex.EncodeToString(b)
}

// generateID returns a prefixed ID like "usr_" + 16 hex chars.
func (s *store) generateID(prefix string) string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("twinapi: failed to generate id: %v", err))
	}
	return prefix + hex.EncodeToString(b)
}

func (s *store) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()

	// regenerate jwt secret on reset
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		panic(fmt.Sprintf("twinapi: failed to generate jwt secret: %v", err))
	}
	s.jwtSecret = secret
	s.clock = time.Now()
	s.users = make(map[string]*User)
	s.sessions = make(map[string]*Session)
	s.teams = make(map[string]*Team)
	s.repos = make(map[string]*Repo)
	s.deviceCodes = make(map[string]*DeviceCode)
	s.calls = nil
	s.faults = make(map[string]*EndpointFault)
}
