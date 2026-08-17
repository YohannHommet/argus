package httpapi

import (
	"embed"
	"io"
	"io/fs"
	"net/http"
	"path"
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
		// m24 audit finding: before this, every path that missed the
		// /assets/* mount above — including a genuinely missing root file
		// like /favicon.svg — fell straight through to serveIndex, so the
		// browser got the whole SPA document back as 200 text/html
		// wherever it expected a specific asset. A root-level path with a
		// file extension (isRootStaticAssetPath) is now served through the
		// FileServer if it really exists there, and 404s (not 200-with-
		// index.html) if it doesn't; a client-side route like /sessions/abc
		// has no extension on its last segment, so it still falls through
		// to serveIndex exactly as before.
		if isRootStaticAssetPath(req.URL.Path) {
			if rootStaticAssetExists(assets, req.URL.Path) {
				fileServer.ServeHTTP(w, req)
				return
			}
			problemNotFoundHandler(w, req)
			return
		}
		serveIndex(w, req, assets)
	})
}

// isRootStaticAssetPath reports whether p looks like a request for a real
// static file served straight from the SPA's document root (e.g.
// /favicon.svg, /robots.txt) rather than a client-side route (e.g.
// /sessions/abc): a last path segment containing a dot. Every one of
// Argus's actual client-side routes is an id/keyword segment with no dot in
// it, so this heuristic never misclassifies a real route as a missing
// asset — the same convention static-SPA servers (webpack-dev-server's
// historyApiFallback, `serve`, ...) use for exactly this disambiguation.
func isRootStaticAssetPath(p string) bool {
	return strings.Contains(path.Base(p), ".")
}

// rootStaticAssetExists reports whether p names a real, non-directory file
// in assets, given p is already known to look like a root static asset
// request (isRootStaticAssetPath).
func rootStaticAssetExists(assets fs.FS, p string) bool {
	name := strings.TrimPrefix(p, "/")
	if name == "" {
		return false
	}
	info, err := fs.Stat(assets, name)
	return err == nil && !info.IsDir()
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
	defer func() { _ = f.Close() }()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}
