package web

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"

	"trafficmonitor/internal/snapshot"
	"trafficmonitor/internal/store"
)

// Read-only integration API for Home Assistant, Prometheus, Node-RED, scripts…
// Every endpoint is GET-only and works in live-only mode, except /usage for
// ranges other than "today" and /history, which need the database.

func (s *Server) v1() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/{$}", s.v1Index)
	mux.HandleFunc("GET /api/v1/status", s.v1Status)
	mux.HandleFunc("GET /api/v1/summary", s.v1Summary)
	mux.HandleFunc("GET /api/v1/devices", s.v1Devices)
	mux.HandleFunc("GET /api/v1/devices/{id}", s.v1Device)
	mux.HandleFunc("GET /api/v1/interfaces", s.v1Interfaces)
	mux.HandleFunc("GET /api/v1/interfaces/{name}", s.v1Interface)
	mux.HandleFunc("GET /api/v1/networks", s.v1Networks)
	mux.HandleFunc("GET /api/v1/networks/{name}", s.v1Network)
	mux.HandleFunc("GET /api/v1/usage", s.v1Usage)
	mux.HandleFunc("GET /api/v1/history", s.v1History)
	mux.HandleFunc("GET /metrics", s.metrics)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			apiError(w, http.StatusMethodNotAllowed, "this API is read-only (GET)")
			return
		}
		apiError(w, http.StatusNotFound, "unknown endpoint, see /api/v1/")
	})
	return s.cors(s.tokenAuth(mux))
}

func (s *Server) cors(next http.Handler) http.Handler {
	if !s.opt.API.CORS {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, X-API-Key")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// tokenAuth accepts "Authorization: Bearer <token>", "X-API-Key: <token>" or
// "?token=<token>". With no tokens configured the API is open.
func (s *Server) tokenAuth(next http.Handler) http.Handler {
	tokens := s.opt.API.Tokens
	if len(tokens) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("X-API-Key")
		if a := r.Header.Get("Authorization"); got == "" && len(a) > 7 && strings.EqualFold(a[:7], "bearer ") {
			got = strings.TrimSpace(a[7:])
		}
		if got == "" {
			got = r.URL.Query().Get("token")
		}
		ok := false
		for _, t := range tokens {
			if t != "" && subtle.ConstantTimeCompare([]byte(got), []byte(t)) == 1 {
				ok = true
			}
		}
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="traffic-monitor"`)
			apiError(w, http.StatusUnauthorized, "missing or invalid API token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func apiError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func apiJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

// ---------------------------------------------------------------- snapshot

// ---------------------------------------------------------------- handlers

func (s *Server) v1Index(w http.ResponseWriter, r *http.Request) {
	apiJSON(w, map[string]any{
		"name":            "traffic-monitor",
		"mode":            s.src.Mode(),
		"history_enabled": s.db != nil,
		"endpoints": map[string]string{
			"/api/v1/status":            "collector health and mode",
			"/api/v1/summary":           "WAN rates, today's totals, active devices, per-network rates",
			"/api/v1/devices":           "all devices: live rates + today's totals (?network=, ?active=true, ?limit=)",
			"/api/v1/devices/{ip|name}": "one device",
			"/api/v1/interfaces":        "firewall interfaces (WAN/LAN/VLAN/VPN): live rates + today's totals",
			"/api/v1/interfaces/{name}": "one interface, by name (igb0) or label (WAN)",
			"/api/v1/networks":          "per network (LAN, VLANs, VPNs): live rates + today's totals",
			"/api/v1/networks/{name}":   "one network",
			"/api/v1/usage":             "per-device totals for a range (?range=today|yesterday|24h|7d|30d|month|lastmonth|year or ?from=&to= unix, ?network=)",
			"/api/v1/history":           "time series (?range=, ?step=minute|5min|15min|hour|day|week|month, ?ip= | ?network= | ?interface=)",
			"/metrics":                  "Prometheus exposition format",
		},
	})
}

func (s *Server) v1Status(w http.ResponseWriter, r *http.Request) {
	meta, nets, err := s.src.Status(r.Context())
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	n := func(k string) int64 { v, _ := strconv.ParseInt(meta[k], 10, 64); return v }
	exps := []string{}
	if meta["exporters"] != "" {
		exps = strings.Split(meta["exporters"], ", ")
	}
	var uptime int64
	if st := n("collector_started"); st > 0 {
		uptime = time.Now().Unix() - st
	}
	apiJSON(w, map[string]any{
		"version":               s.opt.Version,
		"mode":                  s.src.Mode(),
		"history_enabled":       s.db != nil,
		"collector_alive":       snapshot.CollectorAlive(meta),
		"collector_heartbeat":   n("collector_heartbeat"),
		"collector_uptime_secs": uptime,
		"exporters":             exps,
		"flows_total":           n("flows_total"),
		"packets_total":         n("packets_total"),
		"flows_ignored":         n("flows_ignored"),
		"packets_bad":           n("packets_bad"),
		"packets_refused":       n("packets_refused"),
		"flows_limited":         n("flows_limited"),
		"snmp_enabled":          meta["snmp_enabled"] == "true",
		"live_window_secs":      n("live_window"),
		"networks":              len(nets),
	})
}

func (s *Server) v1Summary(w http.ResponseWriter, r *http.Request) {
	snap, err := s.src.Take(r.Context())
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	active := 0
	var lanBytes int64
	for _, d := range snap.Devices {
		if d.Active {
			active++
		}
		lanBytes += d.TodayLocalBytes
	}
	var top any
	if len(snap.Devices) > 0 && snap.Devices[0].Active {
		top = snap.Devices[0]
	}
	apiJSON(w, map[string]any{
		"time":            snap.Time.Unix(),
		"mode":            s.src.Mode(),
		"collector_alive": snapshot.CollectorAlive(snap.Meta),
		"wan":             snap.WAN,
		"today": map[string]any{
			"since":          snap.Since.Unix(),
			"download_bytes": snap.WAN.TodayDownloadBytes,
			"upload_bytes":   snap.WAN.TodayUploadBytes,
			"local_bytes":    lanBytes,
		},
		"active_devices": active,
		"known_devices":  len(snap.Devices),
		"top_device":     top,
		"networks":       snap.Networks,
	})
}

func (s *Server) v1Devices(w http.ResponseWriter, r *http.Request) {
	snap, err := s.src.Take(r.Context())
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	q := r.URL.Query()
	out := []snapshot.Device{}
	for _, d := range snap.Devices {
		if (q.Get("network") != "" && !strings.EqualFold(d.Network, q.Get("network"))) || (q.Get("active") == "true" && !d.Active) {
			continue
		}
		out = append(out, d)
	}
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 && n < len(out) {
		out = out[:n]
	}
	apiJSON(w, out)
}

func (s *Server) v1Device(w http.ResponseWriter, r *http.Request) {
	snap, err := s.src.Take(r.Context())
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	id := r.PathValue("id")
	addr, ipErr := netip.ParseAddr(id)
	for _, d := range snap.Devices {
		if (ipErr == nil && d.IP == addr.String()) || strings.EqualFold(d.Name, id) {
			apiJSON(w, d)
			return
		}
	}
	if ipErr == nil {
		// a valid IP that hasn't sent traffic: report zeros so sensors stay available
		apiJSON(w, snapshot.Device{IP: addr.String()})
		return
	}
	apiError(w, http.StatusNotFound, "no device with that IP or name")
}

func (s *Server) v1Interfaces(w http.ResponseWriter, r *http.Request) {
	snap, err := s.src.Take(r.Context())
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if snap.Ifaces == nil {
		snap.Ifaces = []snapshot.Iface{}
	}
	apiJSON(w, snap.Ifaces)
}

func (s *Server) v1Interface(w http.ResponseWriter, r *http.Request) {
	snap, err := s.src.Take(r.Context())
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	name := r.PathValue("name")
	for _, f := range snap.Ifaces {
		if f.Name == name || strings.EqualFold(f.Label, name) {
			apiJSON(w, f)
			return
		}
	}
	apiError(w, http.StatusNotFound, "no interface with that name or label (is SNMP enabled?)")
}

func (s *Server) v1Networks(w http.ResponseWriter, r *http.Request) {
	snap, err := s.src.Take(r.Context())
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if snap.Networks == nil {
		snap.Networks = []snapshot.Network{}
	}
	apiJSON(w, snap.Networks)
}

func (s *Server) v1Network(w http.ResponseWriter, r *http.Request) {
	snap, err := s.src.Take(r.Context())
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, n := range snap.Networks {
		if strings.EqualFold(n.Name, r.PathValue("name")) {
			apiJSON(w, n)
			return
		}
	}
	apiError(w, http.StatusNotFound, "no network with that name")
}

var relRange = regexp.MustCompile(`^(\d+)([hd])$`)

// namedRange resolves ?range= (default today) or explicit ?from=&to= (unix).
func namedRange(r *http.Request) (time.Time, time.Time, string, bool) {
	q := r.URL.Query()
	now := time.Now()
	if q.Get("from") != "" || q.Get("to") != "" {
		from, to, err := rangeParams(r)
		return from, to, "custom", err == nil
	}
	name := q.Get("range")
	if name == "" {
		name = "today"
	}
	today := snapshot.Midnight(now)
	end := now.Add(time.Minute)
	switch name {
	case "today":
		return today, end, name, true
	case "yesterday":
		return today.AddDate(0, 0, -1), today, name, true
	case "month":
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local), end, name, true
	case "lastmonth":
		first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
		return first.AddDate(0, -1, 0), first, name, true
	case "year":
		return time.Date(now.Year(), 1, 1, 0, 0, 0, 0, time.Local), end, name, true
	}
	if m := relRange.FindStringSubmatch(name); m != nil {
		n, _ := strconv.Atoi(m[1])
		if n > 36600 {
			return now, now, name, false // over 100 years: reject rather than overflow
		}
		if m[2] == "h" {
			return now.Add(-time.Duration(n) * time.Hour), end, name, n > 0
		}
		return now.AddDate(0, 0, -n), end, name, n > 0
	}
	return now, now, name, false
}

type v1UsageDevice struct {
	IP            string `json:"ip"`
	Name          string `json:"name"`
	Network       string `json:"network"`
	DownloadBytes int64  `json:"download_bytes"`
	UploadBytes   int64  `json:"upload_bytes"`
	LocalBytes    int64  `json:"local_bytes"`
}

func (s *Server) v1Usage(w http.ResponseWriter, r *http.Request) {
	from, to, name, ok := namedRange(r)
	if !ok {
		apiError(w, http.StatusBadRequest, "bad range; use today|yesterday|24h|7d|30d|month|lastmonth|year|<N>h|<N>d or from/to unix seconds")
		return
	}
	network := r.URL.Query().Get("network")
	var rows []store.HostTotal
	switch {
	case s.db != nil:
		var err error
		rows, err = s.db.TopHosts(r.Context(), from, to, store.Filter{Network: network}, 100000)
		if err != nil {
			apiError(w, http.StatusInternalServerError, err.Error())
			return
		}
		names := s.src.HostNames(r.Context())
		for i := range rows {
			if h, ok := names[rows[i].IP]; ok && h.Display() != "" {
				rows[i].Name = h.Display()
			}
		}
	case name == "today":
		hs, _, since, err := s.src.Today(r.Context())
		if err != nil {
			apiError(w, http.StatusInternalServerError, err.Error())
			return
		}
		from = since
		for _, h := range hs {
			if network == "" || strings.EqualFold(h.Network, network) {
				rows = append(rows, h)
			}
		}
	default:
		apiError(w, http.StatusServiceUnavailable, errNoHistory.Error()+` (range=today still works)`)
		return
	}
	out := map[string]any{"range": name, "from": from.Unix(), "to": to.Unix()}
	devs := []v1UsageDevice{}
	var dl, ul, lan int64
	for _, h := range rows {
		devs = append(devs, v1UsageDevice{h.IP, h.Name, h.Network, h.Rx, h.Tx, h.LanRx + h.LanTx})
		dl, ul, lan = dl+h.Rx, ul+h.Tx, lan+h.LanRx+h.LanTx
	}
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n < len(devs) {
		devs = devs[:n]
	}
	out["download_bytes"], out["upload_bytes"], out["local_bytes"], out["devices"] = dl, ul, lan, devs
	apiJSON(w, out)
}

func (s *Server) v1History(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		apiError(w, http.StatusServiceUnavailable, errNoHistory.Error())
		return
	}
	from, to, name, ok := namedRange(r)
	if !ok {
		apiError(w, http.StatusBadRequest, "bad range")
		return
	}
	q := r.URL.Query()
	step := stepParam(r, from, to)
	var pts []store.Point
	var err error
	iface := q.Get("interface")
	if iface != "" {
		pts, step, err = s.db.IfaceSeries(r.Context(), from, to, step, iface)
	} else {
		pts, step, err = s.db.Series(r.Context(), from, to, step, store.Filter{IP: q.Get("ip"), Network: q.Get("network")})
	}
	if err != nil {
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	points := make([]map[string]any, 0, len(pts))
	for _, p := range pts {
		m := map[string]any{"t": p.T, "time": time.Unix(p.T, 0).Format(time.RFC3339)}
		if iface != "" {
			m["in_bytes"], m["out_bytes"] = p.Rx, p.Tx
		} else {
			m["download_bytes"], m["upload_bytes"], m["local_bytes"] = p.Rx, p.Tx, p.LanRx+p.LanTx
		}
		points = append(points, m)
	}
	apiJSON(w, map[string]any{"range": name, "from": from.Unix(), "to": to.Unix(), "step": step, "points": points})
}
