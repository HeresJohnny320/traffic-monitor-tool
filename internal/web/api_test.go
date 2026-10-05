package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"trafficmonitor/internal/config"
	"trafficmonitor/internal/live"
	"trafficmonitor/internal/snapshot"
	"trafficmonitor/internal/store"
)

// testState is collector memory with a little traffic in it.
func testState() *live.State {
	st := live.New()
	st.SetNetworks([]store.Network{{Name: "LAN", Kind: "lan", CIDRs: "192.168.1.0/24"}, {Name: "IoT", Kind: "vlan"}})
	st.SetMeta(map[string]string{"collector_heartbeat": strconv.FormatInt(time.Now().Unix(), 10)})
	st.AddHost("192.168.1.10", "Gaming PC", "LAN", "lan", 5000, 500, 0, 0)
	st.AddHost("192.168.1.11", "", "LAN", "lan", 100, 10, 0, 0)
	st.SetHosts([]store.LiveHostRow{{HostTotal: store.HostTotal{IP: "192.168.1.10", Name: "Gaming PC", Network: "LAN", Kind: "lan"}, RxBps: 8e6, TxBps: 1e6}})
	st.SetIfaces([]store.LiveIfaceRow{{Name: "igb0", Label: "WAN", Kind: "wan", InBps: 9e6, OutBps: 2e6}})
	st.AddIface("igb0", "WAN", "wan", 7000, 900)
	return st
}

// liveOnlyServer is a server in live-only mode (no database).
func liveOnlyServer(tokens ...string) http.Handler {
	return New(nil, snapshot.New(nil, testState()), Options{UI: true, API: config.API{Enabled: true, Tokens: tokens}}).Handler()
}

func get(t *testing.T, h http.Handler, method, url string, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, url, nil)
	req.Host = "traffic-monitor.lan"
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestTokenAuth(t *testing.T) {
	h := liveOnlyServer("abc")
	cases := []struct {
		url  string
		hdr  []string
		want int
	}{
		{"/api/v1/summary", nil, 401},
		{"/api/v1/summary", []string{"Authorization", "Bearer wrong"}, 401},
		{"/api/v1/summary", []string{"Authorization", "Bearer abc"}, 200},
		{"/api/v1/summary", []string{"X-API-Key", "abc"}, 200},
		{"/api/v1/summary?token=abc", nil, 200},
		{"/metrics", nil, 401},
		{"/metrics?token=abc", nil, 200},
	}
	for _, c := range cases {
		if got := get(t, h, "GET", c.url, c.hdr...).Code; got != c.want {
			t.Errorf("%s %v: got %d want %d", c.url, c.hdr, got, c.want)
		}
	}
	if got := get(t, h, "POST", "/api/v1/summary?token=abc").Code; got != 405 {
		t.Errorf("POST should be rejected, got %d", got)
	}
}

func TestToggles(t *testing.T) {
	src := snapshot.New(nil, testState())
	// API off: integration endpoints are gone, dashboard still works
	h := New(nil, src, Options{UI: true}).Handler()
	if c := get(t, h, "GET", "/api/v1/summary").Code; c != 404 {
		t.Errorf("api disabled: /api/v1/summary got %d", c)
	}
	if c := get(t, h, "GET", "/metrics").Code; c != 404 {
		t.Errorf("api disabled: /metrics got %d", c)
	}
	if c := get(t, h, "GET", "/api/live").Code; c != 200 {
		t.Errorf("dashboard live got %d", c)
	}
	// UI off: only the API is served
	h = New(nil, src, Options{API: config.API{Enabled: true}}).Handler()
	if c := get(t, h, "GET", "/api/live").Code; c != 404 {
		t.Errorf("ui disabled: /api/live got %d", c)
	}
	if c := get(t, h, "GET", "/api/v1/summary").Code; c != 200 {
		t.Errorf("ui disabled: /api/v1/summary got %d", c)
	}
}

func TestLiveOnlySummary(t *testing.T) {
	h := liveOnlyServer()
	rec := get(t, h, "GET", "/api/v1/summary")
	var s struct {
		Mode string             `json:"mode"`
		WAN  snapshot.WAN       `json:"wan"`
		Act  int                `json:"active_devices"`
		Nets []snapshot.Network `json:"networks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatal(err, rec.Body.String())
	}
	if s.Mode != "live_only" || s.WAN.Source != "snmp" || s.WAN.DownloadBps != 9e6 || s.WAN.TodayDownloadBytes != 7000 || s.Act != 1 {
		t.Fatalf("bad summary: %+v", s)
	}
	if s.Nets[0].Name != "LAN" || s.Nets[0].TodayDownloadBytes != 5100 || s.Nets[0].DownloadBps != 8e6 {
		t.Fatalf("bad network rollup: %+v", s.Nets[0])
	}

	// device lookups by IP and by name, and a valid-but-unseen IP
	for _, u := range []string{"/api/v1/devices/192.168.1.10", "/api/v1/devices/gaming%20pc"} {
		var d snapshot.Device
		json.Unmarshal(get(t, h, "GET", u).Body.Bytes(), &d)
		if d.IP != "192.168.1.10" || !d.Active || d.TodayDownloadBytes != 5000 {
			t.Fatalf("%s: %+v", u, d)
		}
	}
	if c := get(t, h, "GET", "/api/v1/devices/192.168.1.99").Code; c != 200 {
		t.Fatalf("unseen ip: %d", c)
	}
	if c := get(t, h, "GET", "/api/v1/devices/nope").Code; c != 404 {
		t.Fatalf("unknown name: %d", c)
	}
	if c := get(t, h, "GET", "/api/v1/interfaces/wan").Code; c != 200 {
		t.Fatalf("interface by label: %d", c)
	}

	// history needs a database; today's usage doesn't
	if c := get(t, h, "GET", "/api/v1/usage?range=today").Code; c != 200 {
		t.Fatalf("usage today: %d", c)
	}
	for _, u := range []string{"/api/v1/usage?range=7d", "/api/v1/history", "/api/top"} {
		if c := get(t, h, "GET", u).Code; c != 503 {
			t.Fatalf("%s: got %d want 503", u, c)
		}
	}
}

func TestMetrics(t *testing.T) {
	body := get(t, liveOnlyServer(), "GET", "/metrics").Body.String()
	for _, want := range []string{
		"traffic_monitor_history_enabled 0",
		`traffic_monitor_wan_download_bits_per_second{source="snmp"} 9e+06`,
		`traffic_monitor_device_download_bits_per_second{ip="192.168.1.10",name="Gaming PC",network="LAN"} 8e+06`,
		`traffic_monitor_interface_today_in_bytes{interface="igb0",label="WAN",kind="wan"} 7000`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	if escapeLabel("a\"b\\c\nd") != `a\"b\\c\nd` {
		t.Error("label escaping")
	}
}

func TestSecurityGuards(t *testing.T) {
	h := liveOnlyServer()
	// DNS rebinding: a foreign domain in Host is refused, local names are fine
	for host, want := range map[string]int{
		"evil.example.com": 403, "evil.example.com:8080": 403,
		"192.168.1.1:8080": 200, "[fd00::1]:8080": 200, "traffic-monitor": 200,
		"pfsense.home.arpa": 200, "router.lan:8080": 200, "localhost:8080": 200,
	} {
		req := httptest.NewRequest("GET", "/api/live", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("Host %s: got %d want %d", host, rec.Code, want)
		}
	}
	// CSRF: dashboard changes without the custom header are refused, even
	// when the body is a text/plain form crafted to look like JSON
	req := httptest.NewRequest("POST", "/api/hosts/rename", strings.NewReader(`{"IP":"192.168.1.10","Name":"x"}`))
	req.Host = "192.168.1.5"
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Errorf("rename without CSRF header: got %d", rec.Code)
	}
	// security headers are set
	rec = get(t, h, "GET", "/")
	for _, k := range []string{"X-Frame-Options", "Content-Security-Policy", "X-Content-Type-Options"} {
		if rec.Header().Get(k) == "" {
			t.Errorf("missing %s", k)
		}
	}
}
