package web

import (
	"net/http"
	"strings"
)

// handleMiniStatic serves the Mini App SPA under /miniapp/ — the operator's
// custom files (see static_overlay.go) over the embedded ones.
// Gated on the feature flag.
func (s *Server) handleMiniStatic(w http.ResponseWriter, r *http.Request) {
	if s.mini == nil || !s.mini.MiniEnabled() {
		http.NotFound(w, r)
		return
	}
	rel := strings.TrimPrefix(r.URL.Path, "/miniapp/")
	if rel == "" || rel == "index.html" {
		data, err := s.readIndexHTML("miniapp.html")
		if err != nil {
			http.Error(w, "internal", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
		return
	}
	fsys, err := s.staticFS()
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	http.StripPrefix("/miniapp/", http.FileServer(http.FS(fsys))).ServeHTTP(w, r)
}
