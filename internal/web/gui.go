package web

import (
	"crypto/subtle"
	"io/fs"
	"net/http"
	"regexp"
	"strings"
)

// The dashboard can also be shown inside the pfSense/OPNsense web UI
// (Status → Traffic Monitor / Reporting → Traffic Monitor). install.sh puts a
// proxy page on the firewall (gui/traffic_monitor.php) that checks the
// firewall login and forwards the request here with a shared secret, so:
//   - the firewall's login replaces this server's basic auth,
//   - the page may be framed by the firewall's own pages,
//   - every URL the dashboard loads must go through the proxy page, because
//     the firewall's web server only runs URLs ending in .php.
const (
	guiTokenHeader = "X-Traffic-Monitor-Gui"      // shared secret from the gui-token file
	guiBaseHeader  = "X-Traffic-Monitor-Base"     // URL prefix of the proxy page, e.g. /traffic_monitor.php?p=
	guiCSRFHeader  = "X-Traffic-Monitor-Gui-Csrf" // OPNsense's CSRF token, sent back by the dashboard as X-CSRFToken
)

// fromGUI reports whether the request came through the firewall's proxy page.
func (s *Server) fromGUI(r *http.Request) bool {
	t := s.opt.GUIToken
	return t != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get(guiTokenHeader)), []byte(t)) == 1
}

var (
	safeBase = regexp.MustCompile(`^/[A-Za-z0-9_./?=-]*$`)
	safeCSRF = regexp.MustCompile(`^[A-Za-z0-9_+/=-]{0,128}$`)
	htmlRefs = regexp.MustCompile(`(href|src)="([a-z0-9_-]+\.(?:css|js))"`)
	jsImport = regexp.MustCompile(`from "\./([a-z0-9_-]+\.js)"`)
	metaTag  = regexp.MustCompile(`<meta name="(tm-base|tm-csrf)" content="">`)
)

// staticFiles serves the dashboard's files. Through the proxy page, the HTML
// and JavaScript get their file and API references pointed at the proxy.
func (s *Server) staticFiles(files fs.FS) http.Handler {
	fileServer := http.FileServerFS(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base, csrf := r.Header.Get(guiBaseHeader), r.Header.Get(guiCSRFHeader)
		if !s.fromGUI(r) || !safeBase.MatchString(base) || !safeCSRF.MatchString(csrf) {
			fileServer.ServeHTTP(w, r)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		isHTML := strings.HasSuffix(name, ".html")
		if !isHTML && !strings.HasSuffix(name, ".js") {
			fileServer.ServeHTTP(w, r)
			return
		}
		b, err := fs.ReadFile(files, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		out := string(b)
		ctype := "text/javascript; charset=utf-8"
		if isHTML {
			ctype = "text/html; charset=utf-8"
			out = htmlRefs.ReplaceAllString(out, `$1="`+base+`$2"`)
			out = metaTag.ReplaceAllStringFunc(out, func(m string) string {
				if strings.Contains(m, "tm-base") {
					return `<meta name="tm-base" content="` + base + `">`
				}
				return `<meta name="tm-csrf" content="` + csrf + `">`
			})
		} else {
			out = jsImport.ReplaceAllString(out, `from "`+base+`$1"`)
		}
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Cache-Control", "no-cache")
		w.Write([]byte(out))
	})
}
