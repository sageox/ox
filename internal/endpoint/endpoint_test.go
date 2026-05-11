package endpoint

import (
	"os"
	"testing"
)

func TestIsProduction(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		want     bool
	}{
		// production endpoints
		{"api.sageox.ai full URL", "https://api.sageox.ai", true},
		{"api.sageox.ai with path", "https://api.sageox.ai/v1", true},
		{"app.sageox.ai", "https://app.sageox.ai", true},
		{"www.sageox.ai", "https://www.sageox.ai", true},
		{"sageox.ai bare", "https://sageox.ai", true},
		{"empty string defaults to production", "", true},
		{"Production constant", Production, true},
		{"Default constant", Default, true},

		// non-production endpoints
		{"staging.sageox.ai", "https://staging.sageox.ai", false},
		{"dev.sageox.ai", "https://dev.sageox.ai", false},
		{"localhost", "http://localhost", false},
		{"localhost with port", "http://localhost:8080", false},
		{"127.0.0.1", "http://127.0.0.1:8080", false},
		{"custom domain", "https://ox.example.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsProduction(tt.endpoint)
			if got != tt.want {
				t.Errorf("IsProduction(%q) = %v, want %v", tt.endpoint, got, tt.want)
			}
		})
	}
}

func TestExtractHost(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		want     string
	}{
		{"full URL", "https://api.sageox.ai", "api.sageox.ai"},
		{"URL with path", "https://api.sageox.ai/v1/foo", "api.sageox.ai"},
		{"URL with port", "https://api.sageox.ai:443", "api.sageox.ai"},
		{"localhost with port", "http://localhost:8080", "localhost"},
		{"bare host", "api.sageox.ai", "api.sageox.ai"},
		{"host:port", "localhost:8080", "localhost"},
		{"uppercase", "HTTPS://API.SAGEOX.AI", "api.sageox.ai"},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractHost(tt.endpoint)
			if got != tt.want {
				t.Errorf("extractHost(%q) = %q, want %q", tt.endpoint, got, tt.want)
			}
		})
	}
}

func TestNormalizeEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		want     string
	}{
		// www. stripping
		{"www full URL", "https://www.sageox.ai", "https://sageox.ai"},
		{"www with path", "https://www.sageox.ai/v1", "https://sageox.ai/v1"},
		{"www staging", "https://www.test.sageox.ai", "https://test.sageox.ai"},
		{"www bare host", "www.sageox.ai", "https://sageox.ai"},
		// Per ox-v5de: third-party hosts (not in the sageox.ai family) keep
		// their subdomain prefix — silently rewriting them caused tokens
		// to be mis-keyed against a non-existent parent domain.
		{"www enterprise (third-party preserved)", "https://www.sageox.walmart.com", "https://www.sageox.walmart.com"},

		// api. stripping
		{"api full URL", "https://api.sageox.ai", "https://sageox.ai"},
		{"api with path", "https://api.sageox.ai/v1/repos", "https://sageox.ai/v1/repos"},
		{"api staging", "https://api.test.sageox.ai", "https://test.sageox.ai"},
		{"api bare host", "api.sageox.ai", "https://sageox.ai"},

		// app. stripping
		{"app full URL", "https://app.sageox.ai", "https://sageox.ai"},
		{"app staging", "https://app.test.sageox.ai", "https://test.sageox.ai"},

		// git. stripping
		{"git full URL", "https://git.sageox.ai", "https://sageox.ai"},
		{"git staging", "https://git.test.sageox.ai", "https://test.sageox.ai"},

		// no stripping needed
		{"already clean", "https://sageox.ai", "https://sageox.ai"},
		{"staging no prefix", "https://staging.sageox.ai", "https://staging.sageox.ai"},
		{"localhost", "http://localhost:8080", "http://localhost:8080"},
		{"custom domain", "https://ox.example.com", "https://ox.example.com"},

		// trailing slash removal
		{"trailing slash", "https://sageox.ai/", "https://sageox.ai"},
		{"www trailing slash", "https://www.sageox.ai/", "https://sageox.ai"},

		// bare host with port
		{"bare host with port", "www.sageox.ai:443", "https://sageox.ai:443"},
		{"api bare with port", "api.sageox.ai:443", "https://sageox.ai:443"},

		// edge cases
		{"empty", "", ""},
		{"whitespace", "  https://www.sageox.ai  ", "https://sageox.ai"},

		// double/stacked prefixes (only strip one)
		{"double api prefix", "https://api.api.sageox.ai", "https://api.sageox.ai"},

		// prefix-only inputs
		{"prefix only api.", "api.", ""},

		// git. bare host
		{"git bare host", "git.sageox.ai", "https://sageox.ai"},

		// URL with prefix + port
		{"api full URL with port and path", "https://api.sageox.ai:443/v1", "https://sageox.ai:443/v1"},

		// bare host no prefix (the --endpoint flag bug)
		{"bare host no prefix", "test.sageox.ai", "https://test.sageox.ai"},

		// mixed case
		{"mixed case prefix", "https://API.SageOx.AI", "https://SageOx.AI"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeEndpoint(tt.endpoint)
			if got != tt.want {
				t.Errorf("NormalizeEndpoint(%q) = %q, want %q", tt.endpoint, got, tt.want)
			}
		})
	}
}

func TestNormalizeSlug(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		want     string
	}{
		// port removal
		{"localhost with port", "localhost:8080", "localhost"},
		{"localhost no port", "localhost", "localhost"},
		{"staging with port", "staging.sageox.ai:443", "staging.sageox.ai"},

		// prefix stripping
		{"api prefix", "api.sageox.ai", "sageox.ai"},
		{"www prefix", "www.sageox.ai", "sageox.ai"},
		{"app prefix", "app.sageox.ai", "sageox.ai"},
		{"git prefix", "git.sageox.ai", "sageox.ai"},
		{"git prefix with subdomain", "git.test.sageox.ai", "test.sageox.ai"},
		{"no prefix", "sageox.ai", "sageox.ai"},

		// 127.0.0.1 normalization
		{"127.0.0.1 to localhost", "127.0.0.1", "localhost"},
		{"127.0.0.1 with port", "127.0.0.1:3000", "localhost"},
		{"https 127.0.0.1", "http://127.0.0.1:8080", "localhost"},

		// combined (full URLs with prefixes and ports)
		{"api prefix with port", "https://api.sageox.ai:443", "sageox.ai"},
		{"full URL with path", "https://api.sageox.ai/api/v1", "sageox.ai"},
		{"www with path and port", "https://www.sageox.ai:443/foo", "sageox.ai"},

		// edge cases
		{"empty string", "", ""},
		{"bare domain", "example.com", "example.com"},
		{"custom subdomain preserved", "staging.sageox.ai", "staging.sageox.ai"},
		{"dev subdomain preserved", "dev.example.com", "dev.example.com"},
		{"uppercase normalized", "HTTPS://API.SAGEOX.AI", "sageox.ai"},

		// Per ox-v5de: third-party hosts keep their subdomain prefix to
		// prevent silent token mis-keying. example.com isn't in the
		// sageox.ai family, so api.www.example.com is preserved as-is.
		{"nested prefix api.www (third-party preserved)", "api.www.example.com", "api.www.example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeSlug(tt.endpoint)
			if got != tt.want {
				t.Errorf("NormalizeSlug(%q) = %q, want %q", tt.endpoint, got, tt.want)
			}
		})
	}
}

func TestSanitizeForPath(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		want     string
	}{
		// delegates to NormalizeSlug, so ports are stripped and prefixes normalized
		{"simple hostname", "https://staging.sageox.ai", "staging.sageox.ai"},
		{"localhost with port", "http://localhost:8080", "localhost"},
		{"bare host with port", "localhost:3000", "localhost"},
		{"just hostname", "staging.sageox.ai", "staging.sageox.ai"},
		{"empty", "", ""},
		{"uppercase normalized", "HTTPS://STAGING.SAGEOX.AI", "staging.sageox.ai"},

		// prefix stripping (via NormalizeSlug)
		{"api prefix stripped", "https://api.sageox.ai", "sageox.ai"},
		{"www prefix stripped", "https://www.sageox.ai", "sageox.ai"},
		{"app prefix stripped", "https://app.sageox.ai", "sageox.ai"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeForPath(tt.endpoint)
			if got != tt.want {
				t.Errorf("SanitizeForPath(%q) = %q, want %q", tt.endpoint, got, tt.want)
			}
		})
	}
}

func TestNormalizeEndpoint_Idempotent(t *testing.T) {
	inputs := []string{
		"https://api.sageox.ai",
		"https://www.test.sageox.ai",
		"https://sageox.ai/v1",
		"api.sageox.ai:443",
		"git.sageox.ai",
		"http://localhost:8080",
		"https://staging.sageox.ai",
		"",
		"api.",
	}

	for _, input := range inputs {
		once := NormalizeEndpoint(input)
		twice := NormalizeEndpoint(once)
		if once != twice {
			t.Errorf("NormalizeEndpoint not idempotent for %q: once=%q, twice=%q", input, once, twice)
		}
	}
}

func TestGet(t *testing.T) {
	// save and restore environment
	original := os.Getenv(EnvVar)
	defer func() {
		if original == "" {
			os.Unsetenv(EnvVar)
		} else {
			os.Setenv(EnvVar, original)
		}
	}()

	t.Run("default", func(t *testing.T) {
		os.Unsetenv(EnvVar)
		got := Get()
		if got != Default {
			t.Errorf("Get() = %q, want %q", got, Default)
		}
	})

	t.Run("custom endpoint", func(t *testing.T) {
		os.Setenv(EnvVar, "https://staging.sageox.ai")
		got := Get()
		if got != "https://staging.sageox.ai" {
			t.Errorf("Get() = %q, want %q", got, "https://staging.sageox.ai")
		}
	})

	t.Run("removes trailing slash", func(t *testing.T) {
		os.Setenv(EnvVar, "https://staging.sageox.ai/")
		got := Get()
		if got != "https://staging.sageox.ai" {
			t.Errorf("Get() = %q, want %q (no trailing slash)", got, "https://staging.sageox.ai")
		}
	})

	t.Run("normalizes www prefix", func(t *testing.T) {
		os.Setenv(EnvVar, "https://www.test.sageox.ai")
		got := Get()
		if got != "https://test.sageox.ai" {
			t.Errorf("Get() = %q, want %q", got, "https://test.sageox.ai")
		}
	})

	t.Run("normalizes api prefix", func(t *testing.T) {
		os.Setenv(EnvVar, "https://api.sageox.ai")
		got := Get()
		if got != "https://sageox.ai" {
			t.Errorf("Get() = %q, want %q", got, "https://sageox.ai")
		}
	})

	t.Run("normalizes app prefix", func(t *testing.T) {
		os.Setenv(EnvVar, "https://app.sageox.ai")
		got := Get()
		if got != "https://sageox.ai" {
			t.Errorf("Get() = %q, want %q", got, "https://sageox.ai")
		}
	})

	t.Run("normalizes via LoggedInEndpointsGetter", func(t *testing.T) {
		os.Unsetenv(EnvVar)
		origGetter := LoggedInEndpointsGetter
		defer func() { LoggedInEndpointsGetter = origGetter }()

		LoggedInEndpointsGetter = func() []string {
			return []string{"https://api.sageox.ai"}
		}
		got := Get()
		if got != "https://sageox.ai" {
			t.Errorf("Get() = %q, want %q", got, "https://sageox.ai")
		}
	})

	t.Run("normalizes via ProjectEndpointGetter", func(t *testing.T) {
		os.Unsetenv(EnvVar)
		origGetter := ProjectEndpointGetter
		defer func() { ProjectEndpointGetter = origGetter }()

		ProjectEndpointGetter = func(root string) string {
			return "https://www.test.sageox.ai"
		}
		got := GetForProject("/tmp/fake-project")
		if got != "https://test.sageox.ai" {
			t.Errorf("GetForProject() = %q, want %q", got, "https://test.sageox.ai")
		}
	})
}
