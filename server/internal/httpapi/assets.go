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

// distFS embeds the web build (SPEC §8.1). A placeholder index.html is
// committed for clean checkouts that haven't run `pnpm build` yet.
//
//go:embed assets/dist
var distFS embed.FS

// distRoot is distFS's embed root; re-rooted so paths match SPA's "/" expectation.
const distRoot = "assets/dist"

func embeddedAssets() fs.FS {
	sub, err := fs.Sub(distFS, distRoot)
	if err != nil {
		panic("httpapi: embedded assets: " + err.Error())
	}
	return sub
}

// hashedAssetPattern matches Vite's content-hashed build output (e.g. /assets/index-Ab12Cd34.js).
const hashedAssetPattern = "/assets/*"

// mountSPA wires the SPA: hashed assets get far-future immutable cache headers;
// other non-API paths fall back to index.html (no-cache) for client-side routing.
func mountSPA(r chi.Router, assets fs.FS) {
	fileServer := http.FileServer(http.FS(assets))

	r.Handle(hashedAssetPattern, immutableCache(fileServer))

	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasPrefix(req.URL.Path, "/api/") {
			problemNotFoundHandler(w, req)
			return
		}
		// Assets with extensions (e.g. /favicon.svg) 404 if missing; routes without extensions → index.html (client-side routing).
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

// isRootStaticAssetPath reports whether p looks like a static file (contains a dot
// in the last segment) vs a client-side route. This heuristic distinguishes
// /favicon.svg from /sessions/abc using the same convention as webpack-dev-server.
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

// serveIndex serves index.html with explicit charset and no-cache headers.
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
