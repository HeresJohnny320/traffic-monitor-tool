package config

import (
	"slices"
	"testing"
)

func pfsenseImport() Import {
	return Import{
		Source: "pfSense fw.home.arpa",
		Networks: []ImportNetwork{
			{Name: "LAN", Kind: "lan", CIDRs: []string{"192.168.1.1/24", "fd00:1::/64"}},
			{Name: "IOT", Kind: "vlan", CIDRs: []string{"10.10.10.0/24"}},
			{Name: "broken", Kind: "lan", CIDRs: []string{"nope"}},
		},
		Interfaces: map[string]Interface{"igb0": {Label: "WAN", Kind: "wan"}, "igb1.10": {Label: "IOT", Kind: "vlan"}},
		Hosts:      map[string]string{"192.168.1.200": "nas", "bad-ip": "x", "192.168.1.9": " "},
		SNMP:       &ImportSNMP{Target: "127.0.0.1", Port: 161, Community: "s3cret"},
	}
}

func TestApplyImportFresh(t *testing.T) {
	c, _ := Parse(nil)
	changes := c.ApplyImport(pfsenseImport())
	if len(changes) == 0 {
		t.Fatal("expected changes")
	}
	if c.DefaultNetworks || len(c.Networks) != 2 || c.Networks[0].Name != "LAN" ||
		!slices.Equal(c.Networks[0].CIDRs, []string{"192.168.1.0/24", "fd00:1::/64"}) || c.Networks[1].Kind != "vlan" {
		t.Errorf("networks = %+v", c.Networks)
	}
	if c.Interfaces["igb0"].Kind != "wan" || c.Interfaces["igb1.10"].Label != "IOT" {
		t.Errorf("interfaces = %+v", c.Interfaces)
	}
	if len(c.ImportedHosts) != 1 || c.ImportedHosts["192.168.1.200"] != "nas" {
		t.Errorf("imported hosts = %+v", c.ImportedHosts)
	}
	if !c.SNMP.Enabled || c.SNMP.Target != "127.0.0.1" || c.SNMP.Community != "s3cret" {
		t.Errorf("snmp = %+v", c.SNMP)
	}
	// survives a save/load round trip and is valid
	c2, err := c.Clone()
	if err != nil || len(c2.Networks) != 2 || len(c2.Networks[0].Prefixes) != 2 || c2.ImportedHosts["192.168.1.200"] != "nas" {
		t.Fatalf("clone = %+v, %v", c2, err)
	}
	// the same import again changes nothing
	if ch := c2.ApplyImport(pfsenseImport()); len(ch) != 0 {
		t.Errorf("second import changed %v", ch)
	}
}

func TestApplyImportKeepsUserSettings(t *testing.T) {
	c, err := Parse([]byte(`
networks:
  - {name: Home, kind: lan, cidr: [192.168.1.0/24]}
interfaces:
  igb0: {label: Internet, kind: wan}
hosts:
  192.168.1.200: My NAS
snmp: {enabled: true, target: 192.168.1.1, community: mine}
`))
	if err != nil {
		t.Fatal(err)
	}
	c.ApplyImport(pfsenseImport())
	// "Home" already holds the LAN's IPv4 subnet, so the LAN stays yours; only IOT is new
	if len(c.Networks) != 2 || c.Networks[0].Name != "Home" || c.Networks[1].Name != "IOT" {
		t.Errorf("networks = %+v", c.Networks)
	}
	if c.Interfaces["igb0"].Label != "Internet" {
		t.Errorf("interface label overwritten: %+v", c.Interfaces["igb0"])
	}
	if c.Hosts["192.168.1.200"] != "My NAS" {
		t.Errorf("fixed name changed: %+v", c.Hosts)
	}
	if c.SNMP.Target != "192.168.1.1" || c.SNMP.Community != "mine" {
		t.Errorf("snmp overwritten: %+v", c.SNMP)
	}
	// a later import updates a firewall network's subnets by name
	im := pfsenseImport()
	im.Networks[1].CIDRs = []string{"10.10.20.0/24"}
	if ch := c.ApplyImport(im); len(ch) != 1 || !slices.Equal(c.Networks[1].CIDRs, []string{"10.10.20.0/24"}) {
		t.Errorf("update by name: %v %+v", ch, c.Networks)
	}
}
