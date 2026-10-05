package config

import (
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Import is what the firewall reports about itself (gui/pfsense/import.inc,
// run by install.sh, a 15-minute cron job, and Settings → Import from pfSense).
type Import struct {
	Source     string               `json:"source"`
	Networks   []ImportNetwork      `json:"networks"`
	Interfaces map[string]Interface `json:"interfaces"` // by real name, e.g. igb0, igb1.10
	Hosts      map[string]string    `json:"hosts"`      // IP → name from DHCP leases, static mappings, DNS overrides
	SNMP       *ImportSNMP          `json:"snmp"`       // the firewall's SNMP agent, when it's usable from here
}

type ImportNetwork struct {
	Name  string   `json:"name"`
	Kind  string   `json:"kind"`
	CIDRs []string `json:"cidr"`
}

type ImportSNMP struct {
	Target    string `json:"target"`
	Port      uint16 `json:"port"`
	Community string `json:"community"`
}

// Clone returns an independent copy of c.
func (c *Config) Clone() (*Config, error) {
	out := *c
	if out.DefaultNetworks {
		out.Networks = nil
	}
	b, err := yaml.Marshal(&out)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// ApplyImport merges what the firewall reported into c and describes what
// changed (nothing = no change). It only adds: networks you named yourself,
// interface labels, fixed device names and an SNMP setup already in place are
// kept. Imported device names live in imported_hosts, replaced on every import
// and used only where no fixed name is set.
func (c *Config) ApplyImport(im Import) []string {
	var changes []string

	var nets []Network
	for _, n := range im.Networks {
		var cidrs []string
		for _, s := range n.CIDRs {
			if p, err := netip.ParsePrefix(strings.TrimSpace(s)); err == nil {
				cidrs = append(cidrs, p.Masked().String())
			}
		}
		if n.Name == "" || len(cidrs) == 0 {
			continue
		}
		kind := n.Kind
		if !slices.Contains([]string{"lan", "vlan", "vpn", "dmz", "other"}, kind) {
			kind = "lan"
		}
		nets = append(nets, Network{Name: n.Name, Kind: kind, CIDRs: cidrs})
	}
	if len(nets) > 0 && c.DefaultNetworks {
		c.Networks, c.DefaultNetworks = nets, false
		changes = append(changes, fmt.Sprintf("networks: %d from the firewall", len(nets)))
	} else {
		for _, n := range nets {
			i := slices.IndexFunc(c.Networks, func(e Network) bool { return e.Name == n.Name })
			switch {
			case i >= 0:
				if !slices.Equal(c.Networks[i].CIDRs, n.CIDRs) {
					c.Networks[i].CIDRs = n.CIDRs
					changes = append(changes, "network "+n.Name+": "+strings.Join(n.CIDRs, ", "))
				}
			case !c.overlaps(n.CIDRs): // a network of yours already holds it: yours wins
				c.Networks = append(c.Networks, n)
				changes = append(changes, "network "+n.Name+" added")
			}
		}
	}

	for name, in := range im.Interfaces {
		if name == "" {
			continue
		}
		if c.Interfaces == nil {
			c.Interfaces = map[string]Interface{}
		}
		cur, ok := c.Interfaces[name]
		switch {
		case !ok:
			c.Interfaces[name] = in
			changes = append(changes, "interface "+name+" ("+in.Label+")")
		case cur.Kind == "" && in.Kind != "":
			cur.Kind = in.Kind
			c.Interfaces[name] = cur
			changes = append(changes, "interface "+name+": "+in.Kind)
		}
	}

	hosts := map[string]string{}
	for ip, name := range im.Hosts {
		if a, err := netip.ParseAddr(ip); err == nil && strings.TrimSpace(name) != "" {
			hosts[a.String()] = strings.TrimSpace(name)
		}
	}
	if !maps.Equal(hosts, c.ImportedHosts) {
		c.ImportedHosts = hosts
		changes = append(changes, fmt.Sprintf("device names: %d", len(hosts)))
	}

	if s := im.SNMP; s != nil && s.Target != "" && !c.SNMP.Enabled {
		c.SNMP.Enabled, c.SNMP.Target, c.SNMP.Community = true, s.Target, s.Community
		if s.Port != 0 {
			c.SNMP.Port = s.Port
		}
		c.SNMP.Version = "2c"
		changes = append(changes, "SNMP: "+s.Target)
	}
	return changes
}

// overlaps reports whether any of the subnets is already one of c's networks.
func (c *Config) overlaps(cidrs []string) bool {
	for _, n := range c.Networks {
		for _, s := range cidrs {
			if slices.Contains(n.CIDRs, s) {
				return true
			}
		}
	}
	return false
}
