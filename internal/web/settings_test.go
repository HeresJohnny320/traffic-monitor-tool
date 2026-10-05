package web

import (
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"trafficmonitor/internal/config"
	"trafficmonitor/internal/snapshot"
)

func settingsServer(t *testing.T, cmd string) (*Server, string, chan struct{}) {
	t.Helper()
	cfg, _ := config.Parse(nil)
	cfg.Web.Listen = "127.0.0.1:0"
	cfg.NetFlow.Listen = "127.0.0.1:0"
	path := filepath.Join(t.TempDir(), "traffic-monitor.yaml")
	reloaded := make(chan struct{}, 1)
	s := New(nil, snapshot.New(nil, testState()), Options{UI: true, Config: cfg, ConfigPath: path, Cmd: cmd,
		Reload: func() { reloaded <- struct{}{} }})
	return s, path, reloaded
}

func put(s *Server, body string, csrf bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest("PUT", "/api/settings", strings.NewReader(body))
	req.Host = "192.168.1.5:8080"
	if csrf {
		req.Header.Set(csrfHeader, "1")
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestSettingsSave(t *testing.T) {
	s, path, reloaded := settingsServer(t, "all")
	ok := `{"config":{"web":{"listen":"127.0.0.1:0","ui":true,"username":"admin","password":"pw"},"netflow":{"listen":"127.0.0.1:0"},
		"networks":[{"name":"LAN","kind":"lan","cidr":["192.168.1.0/24"]}],"api":{"enabled":true,"tokens":["x"]}}}`
	if c := put(s, ok, false).Code; c != 403 {
		t.Fatalf("missing CSRF header: got %d", c)
	}
	if rec := put(s, `{"config":{"networks":[{"name":"LAN","cidr":["nope"]}]}}`, true); rec.Code != 400 {
		t.Fatalf("bad subnet: got %d", rec.Code)
	}
	if rec := put(s, ok, true); rec.Code != 200 {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	select {
	case <-reloaded:
	case <-time.After(2 * time.Second):
		t.Fatal("save did not trigger a reload")
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Networks[0].Name != "LAN" || c.API.Tokens[0] != "x" || c.Web.Password != "pw" {
		t.Fatalf("saved config wrong: %+v", c)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("config mode %v", fi.Mode().Perm())
	}
}

func TestSettingsReadonlyOutsideAll(t *testing.T) {
	s, _, _ := settingsServer(t, "web")
	if c := put(s, `{"config":{}}`, true).Code; c != 403 {
		t.Fatalf("split mode should be read-only, got %d", c)
	}
	s.opt.Cmd = "all"
	s.opt.Config.Web.Settings = false
	if c := put(s, `{"config":{}}`, true).Code; c != 403 {
		t.Fatalf("locked settings should be read-only, got %d", c)
	}
}

func TestSettingsSamePortDifferentAddress(t *testing.T) {
	old, _ := config.Parse([]byte("netflow: {listen: ':0'}"))
	l, err := net.ListenPacket("udp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, port, _ := net.SplitHostPort(l.LocalAddr().String())
	old.NetFlow.Listen = ":" + port
	next := *old
	next.NetFlow.Listen = "127.0.0.1:" + port // e.g. lock NetFlow to localhost on the firewall
	next.Web.Listen = old.Web.Listen
	if err := checkApply(old, &next); err != nil {
		t.Fatalf("same port, narrower address should be accepted: %v", err)
	}
}
