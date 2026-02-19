package uxfriction

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewClient(t *testing.T) {
	tests := []struct {
		name           string
		cfg            ClientConfig
		wantSampleRate float64
	}{
		{
			name: "default config",
			cfg: ClientConfig{
				Endpoint: "https://api.example.com",
				Version:  "1.0.0",
			},
			wantSampleRate: 1.0,
		},
		{
			name: "with auth func",
			cfg: ClientConfig{
				Endpoint: "https://api.example.com",
				Version:  "1.0.0",
				AuthFunc: func() string { return "token123" },
			},
			wantSampleRate: 1.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(tt.cfg)

			if client == nil {
				t.Fatal("NewClient returned nil")
			}

			if client.SampleRate() != tt.wantSampleRate {
				t.Errorf("SampleRate() = %v, want %v", client.SampleRate(), tt.wantSampleRate)
			}

			if client.config.Timeout == 0 {
				t.Error("Timeout should have default value")
			}
		})
	}
}

func TestClient_Submit(t *testing.T) {
	tests := []struct {
		name           string
		events         []FrictionEvent
		serverStatus   int
		serverHeaders  map[string]string
		wantEventCount int
		wantErr        bool
	}{
		{
			name:           "empty events (heartbeat)",
			events:         []FrictionEvent{},
			serverStatus:   http.StatusOK,
			wantEventCount: 0,
		},
		{
			name: "single event",
			events: []FrictionEvent{
				{Kind: "unknown-command", Input: "ox foo"},
			},
			serverStatus:   http.StatusOK,
			wantEventCount: 1,
		},
		{
			name: "multiple events",
			events: []FrictionEvent{
				{Kind: "unknown-command", Input: "ox foo"},
				{Kind: "unknown-flag", Input: "ox bar --baz"},
			},
			serverStatus:   http.StatusOK,
			wantEventCount: 2,
		},
		{
			name: "server returns sample rate",
			events: []FrictionEvent{
				{Kind: "unknown-command", Input: "ox foo"},
			},
			serverStatus:  http.StatusOK,
			serverHeaders: map[string]string{"X-SageOx-Sample-Rate": "0.5"},
		},
		{
			name: "server returns retry-after",
			events: []FrictionEvent{
				{Kind: "unknown-command", Input: "ox foo"},
			},
			serverStatus:  http.StatusTooManyRequests,
			serverHeaders: map[string]string{"Retry-After": "60"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var receivedReq SubmitRequest

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// verify request
				if r.Method != http.MethodPost {
					t.Errorf("Method = %v, want POST", r.Method)
				}
				if r.URL.Path != "/api/v1/cli/friction" {
					t.Errorf("Path = %v, want /api/v1/cli/friction", r.URL.Path)
				}
				if ct := r.Header.Get("Content-Type"); ct != "application/json" {
					t.Errorf("Content-Type = %v, want application/json", ct)
				}

				// decode request body
				if err := json.NewDecoder(r.Body).Decode(&receivedReq); err != nil {
					t.Errorf("Failed to decode request body: %v", err)
				}

				// set response headers
				for k, v := range tt.serverHeaders {
					w.Header().Set(k, v)
				}

				w.WriteHeader(tt.serverStatus)
			}))
			defer server.Close()

			client := NewClient(ClientConfig{
				Endpoint: server.URL,
				Version:  "0.5.0",
			})

			resp, err := client.Submit(context.Background(), tt.events, nil)

			if tt.wantErr {
				if err == nil {
					t.Error("Submit() should have returned error")
				}
				return
			}

			if err != nil {
				t.Fatalf("Submit() error = %v", err)
			}

			if resp.StatusCode != tt.serverStatus {
				t.Errorf("StatusCode = %v, want %v", resp.StatusCode, tt.serverStatus)
			}

			// verify events were sent
			if len(receivedReq.Events) != len(tt.events) {
				t.Errorf("Sent %d events, want %d", len(receivedReq.Events), len(tt.events))
			}

			// verify version was sent
			if receivedReq.Version != "0.5.0" {
				t.Errorf("Version = %v, want 0.5.0", receivedReq.Version)
			}
		})
	}
}

func TestClient_Submit_Truncation(t *testing.T) {
	var receivedReq SubmitRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedReq)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		Endpoint: server.URL,
		Version:  "0.5.0",
	})

	// create event with oversized fields
	longInput := strings.Repeat("x", 600)
	longError := strings.Repeat("e", 300)

	events := []FrictionEvent{
		{Kind: "unknown-command", Input: longInput, ErrorMsg: longError},
	}

	_, err := client.Submit(context.Background(), events, nil)
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}

	// verify truncation
	if len(receivedReq.Events[0].Input) != MaxInputLength {
		t.Errorf("Input length = %d, want %d", len(receivedReq.Events[0].Input), MaxInputLength)
	}
	if len(receivedReq.Events[0].ErrorMsg) != MaxErrorLength {
		t.Errorf("ErrorMsg length = %d, want %d", len(receivedReq.Events[0].ErrorMsg), MaxErrorLength)
	}
}

func TestClient_Submit_MaxEvents(t *testing.T) {
	var receivedReq SubmitRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedReq)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		Endpoint: server.URL,
		Version:  "0.5.0",
	})

	// create more events than max
	events := make([]FrictionEvent, 150)
	for i := range events {
		events[i] = FrictionEvent{Kind: "unknown-command", Input: "test"}
	}

	_, err := client.Submit(context.Background(), events, nil)
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}

	// verify truncation to max
	if len(receivedReq.Events) != MaxEventsPerRequest {
		t.Errorf("Events count = %d, want %d", len(receivedReq.Events), MaxEventsPerRequest)
	}
}

func TestClient_Submit_WithAuth(t *testing.T) {
	var receivedAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		Endpoint: server.URL,
		Version:  "0.5.0",
		AuthFunc: func() string { return "test-token-123" },
	})

	_, err := client.Submit(context.Background(), []FrictionEvent{}, nil)
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}

	if receivedAuth != "Bearer test-token-123" {
		t.Errorf("Authorization = %v, want Bearer test-token-123", receivedAuth)
	}
}

func TestClient_ShouldSend(t *testing.T) {
	tests := []struct {
		name       string
		sampleRate float64
		retryAfter time.Time
		wantSend   bool
	}{
		{
			name:       "default rate allows send",
			sampleRate: 1.0,
			wantSend:   true,
		},
		{
			name:       "zero rate blocks send",
			sampleRate: 0.0,
			wantSend:   false,
		},
		{
			name:       "retry-after in future blocks send",
			sampleRate: 1.0,
			retryAfter: time.Now().Add(1 * time.Hour),
			wantSend:   false,
		},
		{
			name:       "retry-after in past allows send",
			sampleRate: 1.0,
			retryAfter: time.Now().Add(-1 * time.Hour),
			wantSend:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(ClientConfig{
				Endpoint: "https://api.example.com",
				Version:  "0.5.0",
			})

			// set internal state
			client.mu.Lock()
			client.sampleRate = tt.sampleRate
			client.retryAfter = tt.retryAfter
			client.mu.Unlock()

			got := client.ShouldSend()
			if got != tt.wantSend {
				t.Errorf("ShouldSend() = %v, want %v", got, tt.wantSend)
			}
		})
	}
}

func TestClient_UpdateFromHeaders(t *testing.T) {
	tests := []struct {
		name           string
		headers        map[string]string
		wantSampleRate float64
		wantRetryAfter bool
	}{
		{
			name:           "no headers",
			headers:        map[string]string{},
			wantSampleRate: 1.0, // unchanged
		},
		{
			name:           "sample rate header",
			headers:        map[string]string{"X-SageOx-Sample-Rate": "0.25"},
			wantSampleRate: 0.25,
		},
		{
			name:           "retry-after seconds",
			headers:        map[string]string{"Retry-After": "120"},
			wantSampleRate: 1.0,
			wantRetryAfter: true,
		},
		{
			name: "both headers",
			headers: map[string]string{
				"X-SageOx-Sample-Rate": "0.5",
				"Retry-After":          "60",
			},
			wantSampleRate: 0.5,
			wantRetryAfter: true,
		},
		{
			name:           "invalid sample rate ignored",
			headers:        map[string]string{"X-SageOx-Sample-Rate": "invalid"},
			wantSampleRate: 1.0, // unchanged
		},
		{
			name:           "sample rate out of range ignored",
			headers:        map[string]string{"X-SageOx-Sample-Rate": "1.5"},
			wantSampleRate: 1.0, // unchanged (>1.0)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tt.headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			client := NewClient(ClientConfig{
				Endpoint: server.URL,
				Version:  "0.5.0",
			})

			_, err := client.Submit(context.Background(), []FrictionEvent{}, nil)
			if err != nil {
				t.Fatalf("Submit() error = %v", err)
			}

			if client.SampleRate() != tt.wantSampleRate {
				t.Errorf("SampleRate() = %v, want %v", client.SampleRate(), tt.wantSampleRate)
			}

			hasRetryAfter := !client.RetryAfter().IsZero()
			if hasRetryAfter != tt.wantRetryAfter {
				t.Errorf("RetryAfter set = %v, want %v", hasRetryAfter, tt.wantRetryAfter)
			}
		})
	}
}

func TestClient_Reset(t *testing.T) {
	client := NewClient(ClientConfig{
		Endpoint: "https://api.example.com",
		Version:  "0.5.0",
	})

	// modify state
	client.mu.Lock()
	client.sampleRate = 0.1
	client.retryAfter = time.Now().Add(1 * time.Hour)
	client.mu.Unlock()

	// verify modified
	if client.SampleRate() == 1.0 {
		t.Error("SampleRate should have been modified")
	}

	// reset
	client.Reset()

	// verify reset
	if client.SampleRate() != 1.0 {
		t.Errorf("SampleRate() after Reset = %v, want 1.0", client.SampleRate())
	}
	if !client.RetryAfter().IsZero() {
		t.Error("RetryAfter should be zero after Reset")
	}
}

func TestClient_Submit_SendsClientVersionHeader(t *testing.T) {
	var receivedVersion string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedVersion = r.Header.Get("X-Client-Version")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		Endpoint: server.URL,
		Version:  "0.9.0",
	})

	_, err := client.Submit(context.Background(), []FrictionEvent{}, nil)
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}

	if receivedVersion != "0.9.0" {
		t.Errorf("X-Client-Version = %q, want %q", receivedVersion, "0.9.0")
	}
}

func TestClient_Submit_SendsCatalogVersionHeader(t *testing.T) {
	var receivedCatalogVersion string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedCatalogVersion = r.Header.Get("X-Catalog-Version")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		Endpoint: server.URL,
		Version:  "0.9.0",
	})

	// with opts
	opts := &SubmitOptions{CatalogVersion: "v2026-01-17-001"}
	_, err := client.Submit(context.Background(), []FrictionEvent{}, opts)
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}

	if receivedCatalogVersion != "v2026-01-17-001" {
		t.Errorf("X-Catalog-Version = %q, want %q", receivedCatalogVersion, "v2026-01-17-001")
	}
}

func TestClient_Submit_NoCatalogVersionHeaderWhenNilOpts(t *testing.T) {
	var receivedCatalogVersion string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedCatalogVersion = r.Header.Get("X-Catalog-Version")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		Endpoint: server.URL,
		Version:  "0.9.0",
	})

	_, err := client.Submit(context.Background(), []FrictionEvent{}, nil)
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}

	if receivedCatalogVersion != "" {
		t.Errorf("X-Catalog-Version = %q, want empty (nil opts)", receivedCatalogVersion)
	}
}

func TestClient_Submit_ParsesCatalogFromResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := FrictionResponse{
			Accepted: 1,
			Catalog: &CatalogData{
				Version: "v2026-01-17-002",
				Tokens: []TokenMapping{
					{Pattern: "stauts", Target: "status", Kind: "unknown-command"},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		Endpoint: server.URL,
		Version:  "0.9.0",
	})

	resp, err := client.Submit(context.Background(), []FrictionEvent{
		{Kind: "unknown-command", Input: "ox stauts"},
	}, nil)
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}

	if resp.Catalog == nil {
		t.Fatal("Catalog should be parsed from response body")
	}
	if resp.Catalog.Version != "v2026-01-17-002" {
		t.Errorf("Catalog.Version = %q, want %q", resp.Catalog.Version, "v2026-01-17-002")
	}
	if len(resp.Catalog.Tokens) != 1 {
		t.Fatalf("Catalog.Tokens length = %d, want 1", len(resp.Catalog.Tokens))
	}
	if resp.Catalog.Tokens[0].Pattern != "stauts" {
		t.Errorf("Catalog.Tokens[0].Pattern = %q, want %q", resp.Catalog.Tokens[0].Pattern, "stauts")
	}
}

func TestClient_Submit_NoCatalogOnNon2xx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// return catalog data in body, but with 429 status
		resp := FrictionResponse{
			Accepted: 0,
			Catalog: &CatalogData{
				Version: "v-should-not-parse",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		Endpoint: server.URL,
		Version:  "0.9.0",
	})

	resp, err := client.Submit(context.Background(), []FrictionEvent{
		{Kind: "unknown-command", Input: "test"},
	}, nil)
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}

	if resp.Catalog != nil {
		t.Errorf("Catalog should be nil on non-2xx response, got version %q", resp.Catalog.Version)
	}
}

func TestMaxEventsPerRequest(t *testing.T) {
	if MaxEventsPerRequest != 100 {
		t.Errorf("MaxEventsPerRequest = %d, want 100", MaxEventsPerRequest)
	}
}
