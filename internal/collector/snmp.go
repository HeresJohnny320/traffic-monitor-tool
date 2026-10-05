package collector

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/gosnmp/gosnmp"

	"trafficmonitor/internal/config"
	"trafficmonitor/internal/live"
	"trafficmonitor/internal/store"
)

const (
	oidIfDescr      = ".1.3.6.1.2.1.2.2.1.2"
	oidIfOperStatus = ".1.3.6.1.2.1.2.2.1.8"
	oidIfInOctets   = ".1.3.6.1.2.1.2.2.1.10"
	oidIfOutOctets  = ".1.3.6.1.2.1.2.2.1.16"
	oidIfName       = ".1.3.6.1.2.1.31.1.1.1.1"
	oidIfHCIn       = ".1.3.6.1.2.1.31.1.1.1.6"
	oidIfHCOut      = ".1.3.6.1.2.1.31.1.1.1.10"
	oidIfHighSpeed  = ".1.3.6.1.2.1.31.1.1.1.15"
	oidIfAlias      = ".1.3.6.1.2.1.31.1.1.1.18"
)

type ifState struct {
	name      string
	in, out   uint64
	at        time.Time
	has       bool
	counter32 bool
}

// SNMPPoller polls IF-MIB octet counters on the firewall to measure total
// traffic per interface (WAN, LAN, VLANs, OpenVPN/WireGuard/IPsec tunnels).
type SNMPPoller struct {
	cfg    *config.Config
	db     *store.DB // nil in live-only mode
	shared *live.State
	info   map[string]store.IfaceInfo
	g      *gosnmp.GoSNMP
	names  map[int]string // ifIndex -> ifName
	up     map[int]bool
	state  map[int]*ifState
	hc     bool
}

func NewSNMPPoller(cfg *config.Config, db *store.DB, state *live.State) *SNMPPoller {
	ver := gosnmp.Version2c
	if cfg.SNMP.Version == "1" {
		ver = gosnmp.Version1
	}
	return &SNMPPoller{
		cfg: cfg, db: db, shared: state, info: map[string]store.IfaceInfo{},
		g: &gosnmp.GoSNMP{
			Target: cfg.SNMP.Target, Port: cfg.SNMP.Port, Community: cfg.SNMP.Community,
			Version: ver, Timeout: 3 * time.Second, Retries: 1, MaxRepetitions: 50,
		},
		state: map[int]*ifState{},
	}
}

func (p *SNMPPoller) walk(oid string) (map[int]gosnmp.SnmpPDU, error) {
	out := map[int]gosnmp.SnmpPDU{}
	if p.g.Context != nil && p.g.Context.Err() != nil {
		return out, p.g.Context.Err()
	}
	fn := func(pdu gosnmp.SnmpPDU) error {
		i := strings.LastIndexByte(pdu.Name, '.')
		var idx int
		fmt.Sscanf(pdu.Name[i+1:], "%d", &idx)
		out[idx] = pdu
		return nil
	}
	var err error
	if p.g.Version == gosnmp.Version1 {
		err = p.g.Walk(oid, fn)
	} else {
		err = p.g.BulkWalk(oid, fn)
	}
	return out, err
}

func classify(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.HasPrefix(n, "ovpn"), strings.HasPrefix(n, "wg"), strings.HasPrefix(n, "tun"),
		strings.HasPrefix(n, "ipsec"), strings.HasPrefix(n, "enc"), strings.HasPrefix(n, "tailscale"),
		strings.HasPrefix(n, "zt"), strings.HasPrefix(n, "gif"), strings.HasPrefix(n, "gre"):
		return "vpn"
	case strings.HasPrefix(n, "pppoe"), strings.HasPrefix(n, "ppp"):
		return "wan"
	case strings.Contains(n, "."), strings.HasPrefix(n, "vlan"):
		return "vlan"
	}
	return "other"
}

func skipIface(name string) bool {
	n := strings.ToLower(name)
	for _, p := range []string{"lo", "pflog", "pfsync", "usbus", "plip"} {
		if rest, ok := strings.CutPrefix(n, p); ok && rest != "" && strings.Trim(rest, "0123456789") == "" {
			return true
		}
	}
	return false
}

// refresh re-reads interface names/labels (ifIndex can change after reboots
// or when tunnels come and go).
func (p *SNMPPoller) refresh(ctx context.Context) error {
	names, err := p.walk(oidIfName)
	if err != nil || len(names) == 0 {
		if names, err = p.walk(oidIfDescr); err != nil {
			return err
		}
	}
	aliases, _ := p.walk(oidIfAlias)
	speeds, _ := p.walk(oidIfHighSpeed)
	status, _ := p.walk(oidIfOperStatus)
	hc, _ := p.walk(oidIfHCIn)
	p.hc = len(hc) > 0

	p.names, p.up = map[int]string{}, map[int]bool{}
	var infos []store.IfaceInfo
	for idx, pdu := range names {
		name := pduString(pdu)
		if name == "" || skipIface(name) {
			continue
		}
		ic, listed := p.cfg.Interfaces[name]
		if p.cfg.SNMP.OnlyListed && !listed {
			continue
		}
		p.names[idx] = name
		if s, ok := status[idx]; !ok || gosnmp.ToBigInt(s.Value).Int64() == 1 {
			p.up[idx] = true
		}
		info := store.IfaceInfo{Name: name, Label: ic.Label, Kind: ic.Kind}
		if info.Label == "" {
			info.Label = pduString(aliases[idx])
		}
		if info.Label == "" {
			info.Label = name
		}
		if info.Kind == "" {
			info.Kind = classify(name)
		}
		if s, ok := speeds[idx]; ok {
			info.Speed = gosnmp.ToBigInt(s.Value).Uint64() * 1_000_000
		}
		infos = append(infos, info)
		p.info[name] = info
	}
	log.Printf("snmp: %d interfaces on %s (64-bit counters: %v)", len(infos), p.g.Target, p.hc)
	if p.db == nil {
		return nil
	}
	return p.db.UpsertIfaces(ctx, infos)
}

func pduString(p gosnmp.SnmpPDU) string {
	if b, ok := p.Value.([]byte); ok {
		return strings.TrimSpace(string(b))
	}
	if s, ok := p.Value.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func (p *SNMPPoller) Run(ctx context.Context) {
	// cancels in-flight requests, so shutdown and settings changes don't wait
	// for timeouts when the firewall is unreachable
	p.g.Context = ctx
	var connMu sync.Mutex
	go func() {
		<-ctx.Done()
		connMu.Lock()
		if p.g.Conn != nil {
			p.g.Conn.Close() // unblocks a read waiting on an unreachable firewall
		}
		connMu.Unlock()
	}()
	connected := false
	var lastRefresh time.Time
	t := time.NewTicker(p.cfg.SNMP.Interval)
	defer t.Stop()
	for {
		if !connected {
			connMu.Lock()
			err := p.g.Connect()
			connMu.Unlock()
			if err != nil {
				log.Printf("snmp: connect %s: %v", p.g.Target, err)
			} else {
				connected = true
			}
		}
		if connected && (p.names == nil || time.Since(lastRefresh) > 5*time.Minute) {
			if err := p.refresh(ctx); err != nil && ctx.Err() == nil {
				log.Printf("snmp: read interfaces: %v", err)
			} else {
				lastRefresh = time.Now()
			}
		}
		if connected && p.names != nil {
			if err := p.poll(ctx); err != nil && ctx.Err() == nil {
				log.Printf("snmp: poll: %v", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (p *SNMPPoller) poll(ctx context.Context) error {
	inOID, outOID := oidIfInOctets, oidIfOutOctets
	if p.hc {
		inOID, outOID = oidIfHCIn, oidIfHCOut
	}
	ins, err := p.walk(inOID)
	if err != nil {
		return err
	}
	outs, err := p.walk(outOID)
	if err != nil {
		return err
	}
	now := time.Now()
	minute := now.Unix() - now.Unix()%60
	var buckets []store.IfaceBucket
	var rates []store.LiveIface
	var mem []store.LiveIfaceRow
	for idx, name := range p.names {
		ip, ok1 := ins[idx]
		op, ok2 := outs[idx]
		if !ok1 || !ok2 {
			continue
		}
		in, out := gosnmp.ToBigInt(ip.Value).Uint64(), gosnmp.ToBigInt(op.Value).Uint64()
		s := p.state[idx]
		if s == nil || s.name != name {
			s = &ifState{name: name}
			p.state[idx] = s
		}
		if s.has {
			dIn, okIn := delta(s.in, in, !p.hc)
			dOut, okOut := delta(s.out, out, !p.hc)
			secs := now.Sub(s.at).Seconds()
			if okIn && okOut && secs > 0 {
				if dIn > 0 || dOut > 0 {
					buckets = append(buckets, store.IfaceBucket{TS: minute, Name: name, In: dIn, Out: dOut})
				}
				if p.up[idx] || dIn > 0 || dOut > 0 {
					r := store.LiveIface{Name: name, InBps: float64(dIn) * 8 / secs, OutBps: float64(dOut) * 8 / secs}
					rates = append(rates, r)
					m := p.info[name]
					mem = append(mem, store.LiveIfaceRow{Name: name, Label: m.Label, Kind: m.Kind, Speed: int64(m.Speed), InBps: r.InBps, OutBps: r.OutBps})
				}
				if dIn > 0 || dOut > 0 {
					m := p.info[name]
					p.shared.AddIface(name, m.Label, m.Kind, dIn, dOut)
				}
			}
		}
		s.in, s.out, s.at, s.has = in, out, now, true
	}
	store.SortIfaces(mem)
	p.shared.SetIfaces(mem)
	if p.db == nil {
		return nil
	}
	if err := p.db.WriteIfaces(ctx, buckets); err != nil {
		return err
	}
	return p.db.SetLiveIfaces(ctx, rates)
}

// delta handles 32-bit counter wrap; a decrease in a 64-bit counter means the
// interface was reset, so that sample is skipped.
func delta(prev, cur uint64, wrap32 bool) (uint64, bool) {
	if cur >= prev {
		return cur - prev, true
	}
	if wrap32 && prev <= 0xFFFFFFFF {
		return cur + (1 << 32) - prev, true
	}
	return 0, false
}
