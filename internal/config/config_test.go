package config

import (
	"os"
	"path/filepath"
	"testing"
)

func load(t *testing.T, yaml string) *Config {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.yaml")
	os.WriteFile(p, []byte("networks:\n  - {name: LAN, cidr: [192.168.1.0/24]}\n"+yaml), 0o600)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDatabaseOffByDefault(t *testing.T) {
	c := load(t, "")
	if c.Database.Enabled {
		t.Fatal("database should be off by default")
	}
	// fresh install: only the live dashboard (and its Settings page) is on
	if !c.Web.UI || !c.Web.Settings || c.API.Enabled || c.MQTT.Enabled || c.SNMP.Enabled || c.ReverseDNS {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	if err := c.CheckOutputs("all"); err != nil {
		t.Fatalf("default config must start: %v", err)
	}
	// turning it on without other settings gives a local SQLite file
	c = load(t, "database:\n  enabled: true\n")
	if !c.Database.Enabled || c.Database.Driver != "sqlite" || c.Database.DSN != "traffic-monitor.db" {
		t.Fatalf("enabled database should default to local sqlite: %+v", c.Database)
	}
}

func TestMissingFileUsesDefaults(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !c.DefaultNetworks || len(c.Networks) != 1 || len(c.Networks[0].Prefixes) != 4 {
		t.Fatalf("expected built-in private networks, got %+v", c.Networks)
	}
}

func TestSaveRoundTrip(t *testing.T) {
	c := load(t, `
snmp: {enabled: true, target: 192.168.1.1, interval: 7s}
interfaces: {igb0: {label: WAN, kind: wan}}
hosts: {192.168.1.10: Gaming PC}
api: {tokens: [abc]}
mqtt: {interval: 30s, device_list: [192.168.1.5]}
`)
	p := filepath.Join(t.TempDir(), "out.yaml")
	if err := Save(p, c); err != nil {
		t.Fatal(err)
	}
	if err := Save(p, c); err != nil { // second save makes a .bak
		t.Fatal(err)
	}
	if _, err := os.Stat(p + ".bak"); err != nil {
		t.Fatal("no backup written")
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("config file mode %v, want 0600", fi.Mode().Perm())
	}
	d, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if d.SNMP.Interval.String() != "7s" || d.MQTT.Interval.String() != "30s" || d.Interfaces["igb0"].Label != "WAN" ||
		d.Hosts["192.168.1.10"] != "Gaming PC" || d.API.Tokens[0] != "abc" || d.Networks[0].Name != "LAN" || len(d.Networks[0].Prefixes) != 1 {
		t.Fatalf("round trip lost data: %+v", d)
	}
}

func TestValidation(t *testing.T) {
	for _, bad := range []string{
		"networks: [{name: LAN, cidr: [not-a-subnet]}]",
		"networks: [{name: '', cidr: [10.0.0.0/8]}]",
		"networks: [{name: A, cidr: [10.0.0.0/8]}, {name: A, cidr: [10.1.0.0/16]}]",
		"snmp: {enabled: true}",
		"database: {driver: mysql}",
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("expected error for %s", bad)
		}
	}
}
