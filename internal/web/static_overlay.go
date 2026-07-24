package web

import (
	"io/fs"
	"os"
	"path/filepath"
)

// Кастомная статика: оператор может смонтировать в контейнер папку со своими
// файлами (по умолчанию /custom, переопределяется env CUSTOM_STATIC_DIR) и
// перекрыть вшитый дизайн мини-аппа и веб-кабинета. Правила:
//
//   - любой файл из папки перекрывает одноимённый вшитый (index.html,
//     qrcode.min.js, а также любые новые ассеты — картинки, css, js);
//   - miniapp.html (если есть) отдаётся ТОЛЬКО мини-аппу вместо index.html;
//   - cabinet.html (если есть) отдаётся ТОЛЬКО веб-кабинету вместо index.html;
//   - чего в папке нет — берётся из вшитой статики, как раньше.
//
// Файлы читаются с диска на каждый запрос, поэтому правки видны сразу, без
// перезапуска. Серверные подстановки кабинета (заголовок, описание, favicon,
// anti-fingerprint) применяются и к кастомному HTML — если в нём нет
// соответствующих маркеров, они просто ничего не меняют.

// SetStaticDir wires the operator-mounted custom static directory. Empty or
// missing dir disables the overlay (embedded files are served as before).
func (s *Server) SetStaticDir(dir string) { s.staticDir = dir }

// overlayFS serves files from the custom dir when present there, falling back
// to the embedded base FS. Path safety: http.FileServer only passes cleaned
// fs.ValidPath names, and os.DirFS refuses to escape its root.
type overlayFS struct {
	dir  string // may be empty → pure fallback
	base fs.FS
}

func (o overlayFS) Open(name string) (fs.File, error) {
	if o.dir != "" && fs.ValidPath(name) {
		if f, err := os.DirFS(o.dir).Open(name); err == nil {
			return noDir(f)
		}
	}
	f, err := o.base.Open(name)
	if err != nil {
		return nil, err
	}
	return noDir(f)
}

// noDir refuses to serve directories: http.FileServer would otherwise render a
// listing of the operator's custom dir. Files are unaffected.
func noDir(f fs.File) (fs.File, error) {
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		_ = f.Close()
		if err == nil {
			err = fs.ErrNotExist
		}
		return nil, err
	}
	return f, nil
}

// staticFS returns the SPA asset filesystem: custom dir over embedded files.
func (s *Server) staticFS() (fs.FS, error) {
	sub, err := fs.Sub(miniStaticFS, "miniapp_static")
	if err != nil {
		return nil, err
	}
	return overlayFS{dir: s.staticDir, base: sub}, nil
}

// readIndexHTML returns the SPA entry page. `specific` is the per-surface
// override filename (miniapp.html / cabinet.html); a generic custom index.html
// is tried next; the embedded index.html is the final fallback.
func (s *Server) readIndexHTML(specific string) ([]byte, error) {
	if s.staticDir != "" {
		for _, n := range []string{specific, "index.html"} {
			if n == "" {
				continue
			}
			if b, err := os.ReadFile(filepath.Join(s.staticDir, n)); err == nil {
				return b, nil
			}
		}
	}
	return miniStaticFS.ReadFile("miniapp_static/index.html")
}
