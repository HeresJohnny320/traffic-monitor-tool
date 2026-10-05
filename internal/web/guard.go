package web

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// localSuffixes are domain suffixes that only resolve inside a home/office
// network. pfSense uses home.arpa by default, OPNsense localdomain.
var localSuffixes = []string{".local", ".lan", ".home", ".home.arpa", ".internal", ".localdomain", ".intranet", ".private", ".corp", ".localhost"}

// hostAllowed blocks DNS rebinding: a malicious website can point its own
// domain at this server's LAN address and then talk to it from the victim's
// browser as if same-origin. Such requests carry the attacker's domain in the
// Host header, so only IPs, local names and configured names are accepted.
func hostAllowed(hostport string, extra []string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
	if host == "" {
		return false
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return true
	}
	if !strings.Contains(host, ".") || host == "localhost" { // single-label LAN name
		return true
	}
	for _, s := range localSuffixes {
		if strings.HasSuffix(host, s) {
			return true
		}
	}
	for _, e := range extra {
		if strings.EqualFold(strings.TrimSpace(e), host) {
			return true
		}
	}
	return false
}

func (s *Server) guardHost(next http.Handler) http.Handler {
	var extra []string
	if s.opt.Config != nil {
		extra = s.opt.Config.Web.AllowedHosts
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostAllowed(r.Host, extra) {
			http.Error(w, "host name "+r.Host+" is not allowed; add it under Settings → Dashboard & security → Allowed host names (web.allowed_hosts)", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// csrfHeader is sent by the dashboard with every change. Browsers only let a
// page add custom headers to same-origin requests (cross-origin ones need a
// CORS preflight this server never approves), so forms or scripts on other
// websites can't make changes on the user's behalf.
const csrfHeader = "X-Traffic-Monitor"

func requireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get(csrfHeader) != "1" {
			http.Error(w, "missing "+csrfHeader+" header", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
