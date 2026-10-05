package web

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"time"

	"gopkg.in/yaml.v3"

	"trafficmonitor/internal/config"
	"trafficmonitor/internal/store"
)

// The Settings page edits traffic-monitor.yaml. Settings are exchanged as the same
// structure as the YAML file (same key names), validated with the same parser,
// saved atomically, and applied by restarting the components in-process.

// settingsMode: "edit", or "readonly" with a reason.
func (s *Server) settingsMode() (string, string) {
	switch {
	case s.opt.Config == nil || s.opt.Reload == nil:
		return "readonly", "Settings can only be changed when running `traffic-monitor all`."
	case s.opt.Cmd != "all":
		return "readonly", "Collector and dashboard run as separate processes: edit traffic-monitor.yaml and restart both."
	case !s.opt.Config.Web.Settings:
		return "readonly", "Editing is locked (web.settings: false in traffic-monitor.yaml)."
	}
	return "edit", ""
}

// toMap renders a config as a generic map with the YAML key names.
func toMap(c *config.Config) (map[string]any, error) {
	out := *c
	if out.DefaultNetworks {
		out.Networks = nil
	}
	b, err := yaml.Marshal(&out)
	if err != nil {
		return nil, err
	}
	m := map[string]any{}
	err = yaml.Unmarshal(b, &m)
	return m, err
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	mode, reason := s.settingsMode()
	if s.opt.Config == nil {
		writeJSON(w, map[string]any{"mode": mode, "reason": reason}, nil)
		return
	}
	m, err := toMap(s.opt.Config)
	if err != nil {
		writeJSON(w, nil, err)
		return
	}
	// the dashboard password is write-only
	if web, ok := m["web"].(map[string]any); ok {
		web["password"] = ""
	}
	_, ifs, _ := s.src.Live(r.Context())
	detected := []map[string]string{}
	for _, f := range ifs {
		detected = append(detected, map[string]string{"name": f.Name, "label": f.Label, "kind": f.Kind})
	}
	writeJSON(w, map[string]any{
		"mode":             mode,
		"reason":           reason,
		"config":           m,
		"password_set":     s.opt.Config.Web.Password != "",
		"auth":             s.opt.User != "" || s.opt.Pass != "",
		"default_networks": s.opt.Config.DefaultNetworks,
		"config_path":      s.opt.ConfigPath,
		"detected_ifaces":  detected,
	}, nil)
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	if mode, reason := s.settingsMode(); mode != "edit" {
		http.Error(w, reason, http.StatusForbidden)
		return
	}
	var body struct {
		Config map[string]any `json:"config"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil || body.Config == nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	raw, err := yaml.Marshal(body.Config)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	next, err := config.Parse(raw)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	old := s.opt.Config
	// keep the existing password unless a new one was typed; clearing the
	// username turns the login off
	if next.Web.Password == "" && next.Web.Username != "" {
		next.Web.Password = old.Web.Password
	}
	if next.Web.Username == "" {
		next.Web.Password = ""
	}
	if err := checkApply(old, next); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := config.Save(s.opt.ConfigPath, next); err != nil {
		http.Error(w, "saving "+s.opt.ConfigPath+": "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "listen": next.Web.Listen, "ui": next.Web.UI}, nil)
	// restart after the response has gone out
	go func() { time.Sleep(300 * time.Millisecond); s.opt.Reload() }()
}

// checkApply catches problems that would stop Traffic Monitor from starting with the
// new settings, so a bad save can't take it down.
func checkApply(old, next *config.Config) error {
	if err := next.CheckOutputs("all"); err != nil {
		return err
	}
	if next.Database.Enabled && (!old.Database.Enabled || old.Database.Driver != next.Database.Driver || old.Database.DSN != next.Database.DSN) {
		db, err := store.Open(next.Database.Driver, next.Database.DSN)
		if err != nil {
			return errors.New("can't use that database: " + err.Error())
		}
		db.Close()
	}
	if portOf(next.NetFlow.Listen) != portOf(old.NetFlow.Listen) {
		c, err := net.ListenPacket("udp", next.NetFlow.Listen)
		if err != nil {
			return errors.New("NetFlow port: " + err.Error())
		}
		c.Close()
	}
	if (next.Web.UI || next.API.Enabled) && portOf(next.Web.Listen) != portOf(old.Web.Listen) {
		l, err := net.Listen("tcp", next.Web.Listen)
		if err != nil {
			return errors.New("dashboard address: " + err.Error())
		}
		l.Close()
	}
	return nil
}

// portOf returns the port of a listen address. Only a port change is
// test-bound: the running instance still holds the old port, so binding a
// different address on the same port would fail even though it's fine.
func portOf(listen string) string {
	if _, p, err := net.SplitHostPort(listen); err == nil {
		return p
	}
	return listen
}
