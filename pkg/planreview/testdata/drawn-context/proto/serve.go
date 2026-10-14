//go:build ignore

// serve.go is the local-renderer prototype for drawn-region capture (ox#1292).
// It serves a stored plan revision the way `ox plan review` serves a plan
// (stored bytes, tool injected before </body>), and the same bytes framed the
// way a sandboxed viewer frames them: iframe sandbox="allow-scripts" plus a
// CSP that denies network, forms and plugins. Nothing here ships in ox.
//
//	go run ./pkg/planreview/testdata/drawn-context/proto/serve.go
//
// prints the base URL, then serves until interrupted:
//
//	/doc/<rev>         top-level page on a loopback origin (localhost mode)
//	/doc/<rev>?embed=1 the document a host frames (adds the sandbox CSP)
//	/host/<rev>        the opaque-origin harness framing /doc/<rev>?embed=1
//	/proto/…, /fixtures/…  this directory and its parent
//
// <rev> names <rev>.html under ../revisions, or under -revisions <dir>.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// sandboxCSP is the document policy the harness proves capture under: inline
// script and style, data: images and fonts, nothing else.
const sandboxCSP = "sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline'; " +
	"style-src 'unsafe-inline'; img-src data:; font-src data:; connect-src 'none'; " +
	"form-action 'none'; base-uri 'none'; object-src 'none'"

// mermaidCDN matches the CDN tag authored plans load Mermaid with. The
// sandbox CSP blocks it, so both modes replace it with the vendored copy ox
// inlines in artifact renders (internal/plan/assets/mermaid.min.js in this
// repository, also 11.6.0): the two modes serve the same bytes and nothing
// loads from the network.
var mermaidCDN = regexp.MustCompile(`<script[^>]+src="https://cdn\.jsdelivr\.net/npm/mermaid@[^"]+"[^>]*></script>`)

func main() {
	_, self, _, _ := runtime.Caller(0)
	protoDir := filepath.Dir(self)
	fixtureDir := filepath.Dir(protoDir)
	addr := flag.String("addr", "127.0.0.1:0", "listen address")
	revDir := flag.String("revisions", filepath.Join(fixtureDir, "revisions"), "directory of <rev>.html plan revisions")
	flag.Parse()
	mermaid := mustRead(filepath.Join(fixtureDir, "..", "..", "..", "..", "internal", "plan", "assets", "mermaid.min.js"))

	mux := http.NewServeMux()
	mux.Handle("/proto/", http.StripPrefix("/proto/", http.FileServer(http.Dir(protoDir))))
	mux.Handle("/fixtures/", http.StripPrefix("/fixtures/", http.FileServer(http.Dir(fixtureDir))))
	mux.HandleFunc("/doc/", func(w http.ResponseWriter, r *http.Request) {
		stored, rev, ok := readRevision(*revDir, strings.TrimPrefix(r.URL.Path, "/doc/"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		page := mermaidCDN.ReplaceAllLiteral(stored, append(append([]byte("<script>"), mermaid...), "</script>"...))
		page = inject(page, toolBundle(protoDir, rev))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Query().Get("embed") == "1" {
			w.Header().Set("Content-Security-Policy", sandboxCSP)
		}
		_, _ = w.Write(page)
	})
	mux.HandleFunc("/host/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/host/")
		_, rev, ok := readRevision(*revDir, name)
		if !ok {
			http.NotFound(w, r)
			return
		}
		cfg, _ := json.Marshal(map[string]string{"name": name, "plan_revision": rev})
		page := strings.Replace(string(mustRead(filepath.Join(protoDir, "host.html"))),
			"</head>", `<script type="application/json" id="ox-host-config">`+string(cfg)+"</script></head>", 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(page))
	})

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("http://%s\n", ln.Addr())
	log.Fatal(http.Serve(ln, mux))
}

// readRevision returns a stored revision's bytes and its plan_revision: the
// SHA-256 of exactly those bytes, the digest a host computes over the same
// plan.html (and the oid an LFS pointer for it carries).
func readRevision(dir, name string) ([]byte, string, bool) {
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		return nil, "", false
	}
	b, err := os.ReadFile(filepath.Join(dir, name+".html"))
	if err != nil {
		return nil, "", false
	}
	sum := sha256.Sum256(b)
	return b, "sha256:" + hex.EncodeToString(sum[:]), true
}

// toolBundle is the injected chrome for this prototype: the renderer's stamp
// of the revision it served, then the document tool.
func toolBundle(protoDir, rev string) []byte {
	cfg, _ := json.Marshal(map[string]string{"plan_revision": rev})
	return []byte(`<script type="application/json" id="ox-draw-config">` + string(cfg) + "</script>\n" +
		`<script id="ox-draw-tool">` + string(mustRead(filepath.Join(protoDir, "draw.js"))) + "</script>\n")
}

// inject inserts bundle before the last </body>, as plan.InjectChrome does.
func inject(page, bundle []byte) []byte {
	i := strings.LastIndex(strings.ToLower(string(page)), "</body>")
	if i < 0 {
		return append(page, bundle...)
	}
	return append(append(append([]byte{}, page[:i]...), bundle...), page[i:]...)
}

func mustRead(path string) []byte {
	b, err := os.ReadFile(path)
	if err != nil {
		log.Fatal(err)
	}
	return b
}
