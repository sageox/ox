package httpisolate

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDefaultTransport_HidesCloseIdleConnectionsAndStillServes(t *testing.T) {
	prev := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = prev })
	DefaultTransport()

	_, closable := http.DefaultTransport.(interface{ CloseIdleConnections() })
	if closable {
		t.Fatal("http.DefaultTransport still exposes CloseIdleConnections; httptest.Server.Close would reach it")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("request through the isolated default transport: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Fatalf("body = %q, want %q", body, "ok")
	}
}
