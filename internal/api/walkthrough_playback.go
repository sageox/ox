package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/sageox/ox/internal/useragent"
)

// ErrWalkthroughPlaybackUnavailable means the server holds no playback data
// for the recording: it is not a Visual walkthrough, or the data has not been
// built yet. It is an expected answer, never a failure.
var ErrWalkthroughPlaybackUnavailable = errors.New("walkthrough playback data is not available for this recording")

// maxWalkthroughPlaybackBytes bounds the playback document. A long walkthrough
// is a few hundred joined moments, far below this; the cap exists so a broken
// or hostile response cannot be buffered without limit.
const maxWalkthroughPlaybackBytes = 4 << 20

// walkthroughImageTimeout bounds one image download.
const walkthroughImageTimeout = 30 * time.Second

// WalkthroughPlayback returns the raw playback document for a recording. The
// caller parses it (and treats every field as untrusted): this layer is only
// transport, so parsing and sanitizing live beside the output they feed.
// Redirects are refused, as for the other walkthrough calls, so the bearer
// credential can never be forwarded to another host.
func (c *RepoClient) WalkthroughPlayback(ctx context.Context, team, recording string) ([]byte, error) {
	if !safeWalkthroughSegment(team) || !safeWalkthroughSegment(recording) {
		return nil, fmt.Errorf("invalid walkthrough scope")
	}
	base, err := recordingsBase(ContextTypeTeam, team)
	if err != nil {
		return nil, err
	}
	req, err := useragent.NewRequest(ctx, http.MethodGet, c.baseURL+base+"/"+recording+"/walkthrough/playback", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.authToken)
	req.Header.Set("Accept", "application/json")
	resp, err := c.noRedirectClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("walkthrough playback: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrWalkthroughPlaybackUnavailable
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, ErrUnauthorized
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return nil, fmt.Errorf("walkthrough playback returned HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxWalkthroughPlaybackBytes+1))
	if err != nil {
		return nil, fmt.Errorf("walkthrough playback: %w", err)
	}
	if len(raw) > maxWalkthroughPlaybackBytes {
		return nil, fmt.Errorf("walkthrough playback exceeds the size limit")
	}
	return raw, nil
}

// DownloadWalkthroughImage fetches one image from a short-lived presigned URL.
//
// The URL is a credential with a TTL, so this function never sends the bearer
// token with it (the signature is the authorization, and a second credential
// would be rejected by the object store), never follows a redirect, and never
// lets the URL reach an error message: net/http embeds it, query string and
// all, in the errors it returns.
func DownloadWalkthroughImage(ctx context.Context, rawURL string, maxBytes int64) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || u.User != nil {
		return nil, fmt.Errorf("image link is not a usable URL")
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !isLoopbackHost(u.Hostname())) {
		return nil, fmt.Errorf("image link must use https")
	}
	req, err := useragent.NewRequest(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("image link is not a usable URL")
	}
	client := &http.Client{
		Timeout:       walkthroughImageTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return nil, fmt.Errorf("download image: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download image: HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("download image: %w", err)
	}
	if int64(len(raw)) > maxBytes {
		return nil, fmt.Errorf("download image: larger than the %d byte limit", maxBytes)
	}
	return raw, nil
}

// isLoopbackHost lets local test servers speak plain http while every real
// host must use https.
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
