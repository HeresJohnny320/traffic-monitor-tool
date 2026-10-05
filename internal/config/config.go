// Package config loads the traffic-monitor YAML configuration.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Database   Database             `yaml:"database"`
	NetFlow    NetFlow              `yaml:"netflow"`
	SNMP       SNMP                 `yaml:"snmp"`
	Networks   []Network            `yaml:"networks"`
	Interfaces map[string]Interface `yaml:"interfaces"`
	Hosts      map[string]string    `yaml:"hosts"`
	// Device names reported by the firewall (DHCP leases, static mappings,
	// DNS overrides); replaced on every import, below `hosts` in priority.
	ImportedHosts map[string]string `yaml:"imported_hosts,omitempty"`
	Retention     Retention         `yaml:"retention"`
	ReverseDNS    bool              `yaml:"reverse_dns"`
	Web           Web               `yaml:"web"`
	API           API               `yaml:"api"`
	MQTT          MQTT              `yaml:"mqtt"`

	// DefaultNetworks is set when no networks were configured and the
	// built-in private ranges are in use.
	DefaultNetworks bool `yaml:"-"`
}

type Database struct {
	// Off by default. Enabled=false is live-only mode: nothing is written anywhere, the
	// dashboard shows only the Live view and the API serves live data and
	// counters since midnight from memory. Requires `traffic-monitor all`.
	Enabled bool   `yaml:"enabled"`
	Driver  string `yaml:"driver"` // sqlite | postgres | mysql (also MariaDB)
	DSN     string `yaml:"dsn"`
}

type NetFlow struct {
	Listen     string `yaml:"listen"`
	LiveWindow int    `yaml:"live_window"` // seconds; match the exporter's active timeout
	// Only accept flows from these addresses/subnets (e.g. the firewall's IP).
	// Empty = accept from anyone who can reach the port.
	AllowedExporters []string `yaml:"allowed_exporters"`
}

type SNMP struct {
	Enabled   bool          `yaml:"enabled"`
	Target    string        `yaml:"target"`
	Port      uint16        `yaml:"port"`
	Community string        `yaml:"community"`
	Version   string        `yaml:"version"` // 1 | 2c
	Interval  time.Duration `yaml:"interval"`
	// Only poll interfaces whose name is listed in `interfaces` (otherwise all
	// interfaces that are up are polled).
	OnlyListed bool `yaml:"only_listed"`
}

type Network struct {
	Name  string   `yaml:"name"`
	Kind  string   `yaml:"kind"` // lan | vlan | vpn | dmz | other
	CIDRs []string `yaml:"cidr"`

	Prefixes []netip.Prefix `yaml:"-"`
}

type Interface struct {
	Label string `yaml:"label"`
	Kind  string `yaml:"kind"` // wan | lan | vlan | vpn | other
}

type Retention struct {
	MinuteDays int `yaml:"minute_days"`
	HourDays   int `yaml:"hour_days"` // 0 = keep forever
	PeerDays   int `yaml:"peer_days"`
}

type Web struct {
	Listen   string `yaml:"listen"`
	UI       bool   `yaml:"ui"`       // false = serve only the API (headless)
	Settings bool   `yaml:"settings"` // false = Settings page is read-only
	// Extra host names the dashboard/API may be reached by (e.g. a reverse
	// proxy's public name). IPs, localhost, single-label names and local
	// suffixes (.lan, .local, .home.arpa, ...) are always allowed. This blocks
	// DNS-rebinding attacks from malicious websites.
	AllowedHosts []string `yaml:"allowed_hosts"`
	Username     string   `yaml:"username"`
	Password     string   `yaml:"password"`
}

// API is the read-only integration API (/api/v1/... and /metrics) for Home
// Assistant, Prometheus, scripts, etc.
type API struct {
	Enabled bool     `yaml:"enabled"`
	Tokens  []string `yaml:"tokens"` // empty = no token required
	CORS    bool     `yaml:"cors"`   // send Access-Control-Allow-Origin: *
}

// MQTT publishes live data to an MQTT broker using Home Assistant MQTT
// discovery, so Traffic Monitor shows up in Home Assistant as devices with sensors.
type MQTT struct {
	Enabled         bool          `yaml:"enabled"`
	Broker          string        `yaml:"broker"` // tcp://host:1883, ssl://host:8883, ws://host:9001
	Username        string        `yaml:"username"`
	Password        string        `yaml:"password"`
	ClientID        string        `yaml:"client_id"`
	TopicPrefix     string        `yaml:"topic_prefix"`     // state topics: <prefix>/...
	DiscoveryPrefix string        `yaml:"discovery_prefix"` // Home Assistant's, normally "homeassistant"
	Interval        time.Duration `yaml:"interval"`
	TLSInsecure     bool          `yaml:"tls_insecure"` // skip certificate verification for ssl://
	// Which client devices get their own Home Assistant device:
	// named (in `hosts:` or renamed in the UI), all, or none.
	Devices    string   `yaml:"devices"`
	DeviceList []string `yaml:"device_list"` // extra IPs or names to always include
	Interfaces bool     `yaml:"interfaces"`  // one HA device per firewall interface
	Networks   bool     `yaml:"networks"`    // one HA device per network (LAN/VLAN/VPN)
}

// Defaults returns the configuration used for anything not set in the file.
// A fresh install has everything off except the live dashboard and its
// Settings page: no storage, no API, no SNMP, no MQTT, no reverse DNS.
func Defaults() *Config {
	return &Config{
		// storage is opt-in; when enabled, a local SQLite file is the default
		Database:  Database{Enabled: false, Driver: "sqlite", DSN: "traffic-monitor.db"},
		NetFlow:   NetFlow{Listen: ":2055", LiveWindow: 60},
		SNMP:      SNMP{Port: 161, Community: "public", Version: "2c", Interval: 5 * time.Second},
		Retention: Retention{MinuteDays: 14, HourDays: 0, PeerDays: 30},
		Web:       Web{Listen: ":8080", UI: true, Settings: true},
		API:       API{Enabled: false},
		MQTT: MQTT{
			Broker: "tcp://localhost:1883", ClientID: "traffic-monitor", TopicPrefix: "traffic-monitor",
			DiscoveryPrefix: "homeassistant", Interval: 10 * time.Second,
			Devices: "named", Interfaces: true, Networks: true,
		},
	}
}

// Load reads the config file. A missing file is not an error: Traffic Monitor then
// starts with defaults and the file is created when settings are saved.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Parse(nil)
	}
	if err != nil {
		return nil, err
	}
	c, err := Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// defaultNetworks is used until the user defines their own: every private
// address range counts as local, so a fresh install shows devices right away.
var defaultNetworks = []Network{{Name: "Local", Kind: "lan", CIDRs: []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7"}}}

// Parse builds a config from YAML on top of Defaults and validates it.
func Parse(raw []byte) (*Config, error) {
	c := Defaults()
	if err := yaml.Unmarshal(raw, c); err != nil {
		return nil, err
	}
	if c.NetFlow.LiveWindow <= 0 {
		c.NetFlow.LiveWindow = 60
	}
	if c.SNMP.Interval < time.Second {
		c.SNMP.Interval = 5 * time.Second
	}
	if c.MQTT.Interval < time.Second {
		c.MQTT.Interval = 10 * time.Second
	}
	switch c.MQTT.Devices {
	case "named", "all", "none":
	default:
		return nil, fmt.Errorf("mqtt.devices must be named, all or none (got %q)", c.MQTT.Devices)
	}
	switch c.Database.Driver {
	case "sqlite", "postgres", "mysql":
	default:
		return nil, fmt.Errorf("database.driver must be sqlite, postgres or mysql (got %q)", c.Database.Driver)
	}
	if c.SNMP.Enabled && c.SNMP.Target == "" {
		return nil, fmt.Errorf("snmp.target (the firewall's IP) is required when SNMP is enabled")
	}
	if c.MQTT.Enabled && c.MQTT.Broker == "" {
		return nil, fmt.Errorf("mqtt.broker is required when MQTT is enabled")
	}
	for _, s := range c.NetFlow.AllowedExporters {
		if _, err := ParseAddrOrPrefix(s); err != nil {
			return nil, fmt.Errorf("netflow.allowed_exporters: %q is not an IP or subnet", s)
		}
	}
	if len(c.Networks) == 0 {
		c.Networks = append([]Network(nil), defaultNetworks...)
		c.DefaultNetworks = true
	}
	seen := map[string]bool{}
	for i := range c.Networks {
		n := &c.Networks[i]
		n.Prefixes = nil
		if n.Name == "" {
			return nil, fmt.Errorf("network #%d needs a name", i+1)
		}
		if seen[n.Name] {
			return nil, fmt.Errorf("network name %q is used twice", n.Name)
		}
		seen[n.Name] = true
		if n.Kind == "" {
			n.Kind = "lan"
		}
		if len(n.CIDRs) == 0 {
			return nil, fmt.Errorf("network %q needs at least one subnet (e.g. 192.168.1.0/24)", n.Name)
		}
		for _, s := range n.CIDRs {
			p, err := netip.ParsePrefix(strings.TrimSpace(s))
			if err != nil {
				return nil, fmt.Errorf("network %q: %q is not a subnet like 192.168.1.0/24", n.Name, s)
			}
			n.Prefixes = append(n.Prefixes, p.Masked())
		}
	}
	return c, nil
}

// ParseAddrOrPrefix accepts "192.168.1.1" or "192.168.1.0/24".
func ParseAddrOrPrefix(s string) (netip.Prefix, error) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		return p.Masked(), err
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// CheckOutputs reports configurations that can't do anything useful for the
// given command (collector | web | all).
func (c *Config) CheckOutputs(cmd string) error {
	runsCollector := cmd == "collector" || cmd == "all"
	runsWeb := (cmd == "web" || cmd == "all") && (c.Web.UI || c.API.Enabled)
	switch {
	case cmd == "web" && !c.Database.Enabled:
		return errors.New("storage is off: `traffic-monitor web` has nothing to read; use `traffic-monitor all` so it shares the collector's memory")
	case cmd == "web" && !runsWeb:
		return errors.New("the dashboard and the API are both off: nothing to serve")
	case !c.Database.Enabled && !runsWeb && !(runsCollector && c.MQTT.Enabled):
		return errors.New("storage, dashboard, API and MQTT are all off: nothing would be stored or shown")
	}
	return nil
}

// Save writes the config atomically (keeping the previous file as .bak).
// The file can hold secrets, so it is only readable by its owner.
func Save(path string, c *Config) error {
	out := *c
	if out.DefaultNetworks {
		out.Networks = nil // keep the built-in default implicit
	}
	body, err := yaml.Marshal(&out)
	if err != nil {
		return err
	}
	data := append([]byte("# Traffic Monitor configuration. Managed by the Settings page; editing by hand is fine too.\n\n"), body...)
	if old, err := os.ReadFile(path); err == nil {
		os.WriteFile(path+".bak", old, 0o600)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
