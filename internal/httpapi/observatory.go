package httpapi

import (
	"A3S/internal/observatory"
	"embed"
	"encoding/json"
	"net/http"
)

//go:embed web/observatory.html web/observatory.css web/observatory.js
var observatoryAssets embed.FS

// Only the login shell and fixed assets are public. State goes through the same
// administrator authentication as /-/metrics, on every poll (including revocation).
func observatoryAsset(w http.ResponseWriter, r *http.Request) bool {
	var file, kind string
	switch r.URL.Path {
	case "/-/observatory", "/-/observatory/":
		file = "observatory.html"
		kind = "text/html; charset=utf-8"
	case "/-/observatory/app.js":
		file = "observatory.js"
		kind = "text/javascript; charset=utf-8"
	case "/-/observatory/style.css":
		file = "observatory.css"
		kind = "text/css; charset=utf-8"
	default:
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	if r.Method != "GET" && r.Method != "HEAD" {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(405)
		return true
	}
	data, err := observatoryAssets.ReadFile("web/" + file)
	if err != nil {
		w.WriteHeader(500)
		return true
	}
	w.Header().Set("Content-Type", kind)
	if r.Method == "GET" {
		_, _ = w.Write(data)
	}
	return true
}
func observatoryState(w http.ResponseWriter, r *http.Request, m *observatory.Monitor) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != "GET" && r.Method != "HEAD" {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(405)
		return
	}
	if m == nil {
		writeXMLError(w, "Observatory is not configured", 503)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method == "GET" {
		_ = json.NewEncoder(w).Encode(m.Snapshot())
	}
}
