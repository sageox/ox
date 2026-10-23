package secretguard

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"
)

const catalogURL = "https://raw.githubusercontent.com/gitleaks/gitleaks/master/config/gitleaks.toml"

// LatestRules deliberately follows upstream on every publication attempt.
// Gitleaks is an intentional always-latest exception to dependency pinning:
// newly issued credential formats must not wait for another ox release.
// Capture remains offline-capable with the bundled catalog, but publication
// must wait if current protections cannot be loaded and understood.
func LatestRules(ctx context.Context) (*RuleSet, error) {
	data, err := LatestCatalog(ctx)
	if err != nil {
		return nil, err
	}
	return NewRules(data)
}

// LatestCatalog returns the complete validated upstream catalog for reproducible
// offline fallback refreshes. It never returns an unvalidated partial response.
func LatestCatalog(ctx context.Context) ([]byte, error) {
	return fetchLatestCatalog(ctx, &http.Client{
		// Bound publication latency; preserve local content for a later retry.
		Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("secret guard: catalog redirects refused")
		},
	}, catalogURL)
}

func fetchLatestRules(ctx context.Context, client *http.Client, url string) (*RuleSet, error) {
	data, err := fetchLatestCatalog(ctx, client, url)
	if err != nil {
		return nil, err
	}
	return NewRules(data)
}

func fetchLatestCatalog(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, errors.New("secret guard: cannot request current rules")
	}
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("secret guard: current rules unavailable; publication pending")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("secret guard: current rules unavailable; publication pending")
	}
	// The catalog is text, normally well below this bound. A broken upstream
	// response must not exhaust memory or turn a truncated catalog into clean.
	const maxCatalogBytes = 4 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxCatalogBytes+1))
	if err != nil || len(data) > maxCatalogBytes {
		return nil, errors.New("secret guard: current rules unreadable; publication pending")
	}
	_, err = NewRules(data)
	if err != nil {
		return nil, errors.New("secret guard: current rules unsupported; publication pending")
	}
	return data, nil
}
