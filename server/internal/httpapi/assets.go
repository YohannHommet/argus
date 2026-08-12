package httpapi

import (
	"embed"
	"io"
	"io/fs"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// distFS embeds the web build (SPEC §8.1: the Dockerfile copies web/dist
// here before compiling argusd). go:embed cannot traverse outside its own
// package directory and fails to compile if the pattern matches no files,
// so assets/dist/index.html is committed as a placeholder for clean
// checkouts that haven't run `pnpm build` yet (see that file's header
// comment).
//
//go:embed assets/dist
var distFS embed.FS

// distRoot is distFS's embed root; embeddedAssets re-roots it so paths
// inside match what the SPA expects at "/" (index.html, not
// assets/dist/index.html).
const distRoot = "assets/dist"

func embeddedAssets() fs.FS {
	sub, err := fs.Sub(distFS, distRoot)
	if err != nil {
		panic("httpapi: embedded assets: " + err.Error())
	}
	return sub
}

// hashedAssetPattern is where Vite emits content-hashed, immutably
// cacheable build output (e.g. /assets/index-Ab12Cd34.js).
const hashedAssetPattern = "/assets/*"

// mountSPA wires the (embedded, or injected for tests) SPA build: hashed
// assets get a far-future immutable cache header and their real content
// type; every other non-API path falls back to index.html with 200 and
// no-cache, since in a client-side-routed SPA an "unknown" path is usually
// a route the server has never heard of, not a missing resource.
func mountSPA(r chi.Router, assets fs.FS) {
	fileServer := http.FileServer(http.FS(assets))

	r.Handle(hashedAssetPattern, immutableCache(fileServer))

	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasPrefix(req.URL.Path, "/api/") {
			problemNotFoundHandler(w, req)
			return
		}
		serveIndex(w, req, assets)
	})
}

func immutableCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		next.ServeHTTP(w, r)
	})
}

// serveIndex serves index.html directly (http.FileServer's content
// sniffing correctly sets text/html for it too, but we set the header
// ourselves so charset and the no-cache header are always explicit).
func serveIndex(w http.ResponseWriter, r *http.Request, assets fs.FS) {
	f, err := assets.Open("index.html")
	if err != nil {
		problemNotFoundHandler(w, r)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}
