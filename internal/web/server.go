// Package web serves the dashboard, its private JSON API, and the read-only
// integration API (/api/v1, /metrics). It never talks to the firewall: live
// data comes from the collector's memory (same process) or the database, and
// history always comes from the database.
package web

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"trafficmonitor/internal/config"
	"trafficmonitor/internal/snapshot"
	"trafficmonitor/internal/store"
)

//go:embed static
var static embed.FS

type Options struct {
	UI         bool // serve the dashboard
	User, Pass string
	API        config.API

	// Settings page: the running config, where to save it, the command
	// Traffic Monitor runs as, and how to apply a change (restart components).
	Config     *config.Config
	ConfigPath string
	Cmd        string
	Reload     func()
	Version    string

	// GUIToken is the secret the firewall's proxy page sends (see gui.go);
	// empty = the dashboard can't be shown inside the firewall's web UI.
	GUIToken string
}

type Server struct {
	db  *store.DB // nil in live-only mode
	src *snapshot.Source
	opt Options

	rdnsMu sync.Mutex
	rdns   map[string]rdnsEntry
}

type rdnsEntry struct {
	name string
	at   time.Time
}

// New creates the web server. db is nil in live-only mode.
func New(db *store.DB, src *snapshot.Source, opt Options) *Server {
	return &Server{db: db, src: src, opt: opt, rdns: map[string]rdnsEntry{}}
}

func (s *Server) Handler() http.Handler {
	root := http.NewServeMux()
	if s.opt.UI {
		root.Handle("/", s.auth(requireCSRF(s.ui())))
	}
	if s.opt.API.Enabled {
		v1 := s.v1()
		root.Handle("/api/v1/", v1)
		root.Handle("/metrics", v1)
	}
	return s.guardHost(s.securityHeaders(root))
}

func (s *Server) ui() http.Handler {
	mux := http.NewServeMux()
	db := s.requireDB
	sub, _ := fs.Sub(static, "static")
	mux.Handle("GET /", s.staticFiles(sub))
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("GET /api/live", s.liveHandler)
	mux.HandleFunc("GET /api/top", db(s.top))
	mux.HandleFunc("GET /api/series", db(s.series))
	mux.HandleFunc("GET /api/peers", db(s.peers))
	mux.HandleFunc("GET /api/ifaces", db(s.ifaceTotals))
	mux.HandleFunc("GET /api/ifaces/series", db(s.ifaceSeries))
	mux.HandleFunc("GET /api/hosts", db(s.hosts))
	mux.HandleFunc("POST /api/hosts/rename", db(s.renameHost))
	mux.HandleFunc("POST /api/ifaces/rename", db(s.renameIface))
	mux.HandleFunc("GET /api/settings", s.getSettings)
	mux.HandleFunc("PUT /api/settings", s.putSettings)
	mux.HandleFunc("POST /api/import", s.importFirewall)
	return mux
}

// requireDB answers 503 for history endpoints in live-only mode.
func (s *Server) requireDB(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.db == nil {
			http.Error(w, errNoHistory.Error(), http.StatusServiceUnavailable)
			return
		}
		h(w, r)
	}
}

var errNoHistory = errors.New("history is disabled (database.enabled: false); only live data is available")

func (s *Server) auth(next http.Handler) http.Handler {
	user, pass := s.opt.User, s.opt.Pass
	if user == "" && pass == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.fromGUI(r) { // the firewall's proxy page has checked the firewall login
			next.ServeHTTP(w, r)
			return
		}
		u, p, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(u), []byte(user)) != 1 ||
			subtle.ConstantTimeCompare([]byte(p), []byte(pass)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="traffic-monitor"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any, err error) {
	if err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, errBadRequest) {
			code = http.StatusBadRequest
		} else if errors.Is(err, sql.ErrNoRows) {
			code = http.StatusNotFound
		} else {
			log.Printf("api: %v", err)
		}
		http.Error(w, err.Error(), code)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

var errBadRequest = errors.New("bad request")

// securityHeaders: no framing (clickjacking), no MIME sniffing, no referrers.
// Through the firewall's proxy page, the firewall's own pages may frame it.
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		frame, ancestors := "DENY", "'none'"
		if s.fromGUI(r) {
			frame, ancestors = "SAMEORIGIN", "'self'"
		}
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", frame)
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; frame-ancestors "+ancestors)
		next.ServeHTTP(w, r)
	})
}

// rangeParams reads from/to (unix seconds); default is the last 24 hours.
func rangeParams(r *http.Request) (time.Time, time.Time, error) {
	to := time.Now()
	from := to.Add(-24 * time.Hour)
	if v := r.URL.Query().Get("from"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return from, to, errBadRequest
		}
		from = time.Unix(n, 0)
	}
	if v := r.URL.Query().Get("to"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return from, to, errBadRequest
		}
		to = time.Unix(n, 0)
	}
	if !from.Before(to) {
		return from, to, errBadRequest
	}
	return from, to, nil
}

func filter(r *http.Request) store.Filter {
	q := r.URL.Query()
	return store.Filter{IP: q.Get("ip"), Network: q.Get("network")}
}

func intParam(r *http.Request, k string, def, max int) int {
	n, err := strconv.Atoi(r.URL.Query().Get(k))
	if err != nil || n <= 0 {
		return def
	}
	return min(n, max)
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	meta, nets, err := s.src.Status(r.Context())
	if err != nil {
		writeJSON(w, nil, err)
		return
	}
	mode, _ := s.settingsMode()
	firstRun, netflowListen := false, ""
	if s.opt.Config != nil {
		firstRun = s.opt.Config.DefaultNetworks // no networks set up yet
		netflowListen = s.opt.Config.NetFlow.Listen
	}
	writeJSON(w, map[string]any{
		"first_run":       firstRun,
		"netflow_listen":  netflowListen,
		"meta":            meta,
		"networks":        nets,
		"collector_alive": snapshot.CollectorAlive(meta),
		"history":         s.db != nil,
		"settings":        mode,
		"auth":            s.opt.User != "" || s.opt.Pass != "",
		"now":             time.Now().Unix(),
	}, nil)
}

func (s *Server) liveHandler(w http.ResponseWriter, r *http.Request) {
	hosts, ifs, err := s.src.Live(r.Context())
	writeJSON(w, map[string]any{"hosts": hosts, "ifaces": ifs, "t": time.Now().Unix()}, err)
}

func (s *Server) top(w http.ResponseWriter, r *http.Request) {
	from, to, err := rangeParams(r)
	if err != nil {
		writeJSON(w, nil, err)
		return
	}
	rows, err := s.db.TopHosts(r.Context(), from, to, filter(r), intParam(r, "limit", 5000, 20000))
	if rows == nil {
		rows = []store.HostTotal{}
	}
	writeJSON(w, rows, err)
}

func (s *Server) series(w http.ResponseWriter, r *http.Request) {
	from, to, err := rangeParams(r)
	if err != nil {
		writeJSON(w, nil, err)
		return
	}
	pts, step, err := s.db.Series(r.Context(), from, to, stepParam(r, from, to), filter(r))
	writeJSON(w, map[string]any{"step": step, "points": pts}, err)
}

func (s *Server) ifaceSeries(w http.ResponseWriter, r *http.Request) {
	from, to, err := rangeParams(r)
	if err != nil {
		writeJSON(w, nil, err)
		return
	}
	pts, step, err := s.db.IfaceSeries(r.Context(), from, to, stepParam(r, from, to), r.URL.Query().Get("name"))
	writeJSON(w, map[string]any{"step": step, "points": pts}, err)
}

// stepParam picks a sensible bucket size when the client asks for "auto".
func stepParam(r *http.Request, from, to time.Time) string {
	if s := r.URL.Query().Get("step"); s != "" && s != "auto" {
		return s
	}
	d := to.Sub(from)
	switch {
	case d <= 3*time.Hour:
		return "minute"
	case d <= 12*time.Hour:
		return "5min"
	case d <= 50*time.Hour:
		return "15min"
	case d <= 8*24*time.Hour:
		return "hour"
	case d <= 120*24*time.Hour:
		return "day"
	case d <= 2*366*24*time.Hour:
		return "week"
	}
	return "month"
}

func (s *Server) ifaceTotals(w http.ResponseWriter, r *http.Request) {
	from, to, err := rangeParams(r)
	if err != nil {
		writeJSON(w, nil, err)
		return
	}
	rows, err := s.db.IfaceTotals(r.Context(), from, to)
	if rows == nil {
		rows = []store.IfaceTotal{}
	}
	writeJSON(w, rows, err)
}

type peerOut struct {
	store.Peer
	Host string `json:"host"`
}

func (s *Server) peers(w http.ResponseWriter, r *http.Request) {
	from, to, err := rangeParams(r)
	if err != nil {
		writeJSON(w, nil, err)
		return
	}
	rows, err := s.db.Peers(r.Context(), from, to, filter(r), intParam(r, "limit", 50, 500))
	if err != nil {
		writeJSON(w, nil, err)
		return
	}
	out := make([]peerOut, len(rows))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16) // bound concurrent DNS lookups
	for i, p := range rows {
		out[i].Peer = p
		wg.Add(1)
		go func(i int, ip string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i].Host = s.lookup(r.Context(), ip)
		}(i, p.Remote)
	}
	wg.Wait()
	writeJSON(w, out, nil)
}

func (s *Server) lookup(ctx context.Context, ip string) string {
	s.rdnsMu.Lock()
	e, ok := s.rdns[ip]
	s.rdnsMu.Unlock()
	if ok && time.Since(e.at) < 6*time.Hour {
		return e.name
	}
	lctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	names, _ := net.DefaultResolver.LookupAddr(lctx, ip)
	name := ""
	if len(names) > 0 {
		name = names[0]
		if name[len(name)-1] == '.' {
			name = name[:len(name)-1]
		}
	}
	s.rdnsMu.Lock()
	if len(s.rdns) > 50000 {
		s.rdns = map[string]rdnsEntry{}
	}
	s.rdns[ip] = rdnsEntry{name, time.Now()}
	s.rdnsMu.Unlock()
	return name
}

func (s *Server) hosts(w http.ResponseWriter, r *http.Request) {
	m, err := s.db.Hosts(r.Context())
	out := make([]store.Host, 0, len(m))
	for _, h := range m {
		out = append(out, h)
	}
	writeJSON(w, out, err)
}

func (s *Server) renameHost(w http.ResponseWriter, r *http.Request) {
	var body struct{ IP, Name string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil || body.IP == "" {
		writeJSON(w, nil, errBadRequest)
		return
	}
	writeJSON(w, map[string]bool{"ok": true}, s.db.RenameHost(r.Context(), body.IP, body.Name))
}

func (s *Server) renameIface(w http.ResponseWriter, r *http.Request) {
	var body struct{ Name, Label string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil || body.Name == "" {
		writeJSON(w, nil, errBadRequest)
		return
	}
	writeJSON(w, map[string]bool{"ok": true}, s.db.RenameIface(r.Context(), body.Name, body.Label))
}
