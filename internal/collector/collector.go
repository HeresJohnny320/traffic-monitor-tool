// Package collector receives NetFlow/IPFIX from the firewall, aggregates it per
// local IP and keeps live state in memory; when a database is configured it
// also writes history to it. Nothing is stored on the firewall.
package collector

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"trafficmonitor/internal/config"
	"trafficmonitor/internal/live"
	"trafficmonitor/internal/netflow"
	"trafficmonitor/internal/store"
)

type hostKey struct {
	ts int64
	ip netip.Addr
}

type peerKey struct {
	ts     int64
	ip     netip.Addr
	remote netip.Addr
	proto  uint8
	port   uint16
}

type counters struct{ rx, tx, lanRx, lanTx uint64 }

// Limits that keep memory bounded even if someone floods the NetFlow port
// with spoofed records (random addresses, endless templates).
const (
	maxDevices      = 20000  // distinct local IPs tracked at once (seen in the last 24h)
	maxHistoryKeys  = 500000 // pending per-minute buckets between DB flushes
	maxPeerKeys     = 200000 // pending per-destination buckets between DB flushes
	maxSpreadMins   = 60     // a flow record is spread over at most this many minutes
	maxExporterKeys = 1000   // distinct exporter addresses remembered for status
)

type Collector struct {
	cfg   *config.Config
	db    *store.DB // nil in live-only mode
	state *live.State
	dec   *netflow.Decoder

	mu      sync.Mutex
	hosts   map[hostKey]*counters
	peers   map[peerKey]*[2]uint64
	seen    map[netip.Addr]int64
	names   map[netip.Addr]string // resolved hostnames
	pending map[netip.Addr]bool   // hosts whose metadata must be (re)written

	flows, packets, dropped, ignored, limited, refused atomic.Uint64
	exporters                                          sync.Map // exporter ip -> last packet unix
	exporterCount                                      atomic.Int64
	allowed                                            []netip.Prefix // netflow.allowed_exporters
}

// New creates a collector. db may be nil (live-only mode: nothing is persisted).
func New(cfg *config.Config, db *store.DB, state *live.State) *Collector {
	c := &Collector{
		cfg:     cfg,
		db:      db,
		state:   state,
		dec:     netflow.NewDecoder(),
		hosts:   map[hostKey]*counters{},
		peers:   map[peerKey]*[2]uint64{},
		seen:    map[netip.Addr]int64{},
		names:   map[netip.Addr]string{},
		pending: map[netip.Addr]bool{},
	}
	for _, s := range cfg.NetFlow.AllowedExporters {
		if p, err := config.ParseAddrOrPrefix(s); err == nil {
			c.allowed = append(c.allowed, p)
		}
	}
	for ip, name := range cfg.Hosts {
		if a, err := netip.ParseAddr(ip); err == nil {
			c.names[a] = name
		}
	}
	return c
}

// network returns the configured local network an address belongs to.
func (c *Collector) network(a netip.Addr) *config.Network {
	for i := range c.cfg.Networks {
		for _, p := range c.cfg.Networks[i].Prefixes {
			if p.Contains(a) {
				return &c.cfg.Networks[i]
			}
		}
	}
	return nil
}

func (c *Collector) Run(ctx context.Context) error {
	nets := make([]store.Network, 0, len(c.cfg.Networks))
	for _, n := range c.cfg.Networks {
		cidrs := ""
		for i, s := range n.CIDRs {
			if i > 0 {
				cidrs += ", "
			}
			cidrs += s
		}
		nets = append(nets, store.Network{Name: n.Name, Kind: n.Kind, CIDRs: cidrs})
	}
	c.state.SetNetworks(nets)
	if c.db != nil {
		if err := c.db.SyncNetworks(ctx, nets); err != nil {
			return err
		}
	} else {
		log.Printf("collector: database disabled, live-only mode (nothing is stored)")
	}

	addr, err := net.ResolveUDPAddr("udp", c.cfg.NetFlow.Listen)
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return fmt.Errorf("netflow listen: %w", err)
	}
	conn.SetReadBuffer(8 << 20)
	log.Printf("netflow: listening on udp %s", c.cfg.NetFlow.Listen)
	go func() { <-ctx.Done(); conn.Close() }()

	var wg sync.WaitGroup
	wg.Add(2)
	if c.db != nil {
		wg.Add(1)
		go func() { defer wg.Done(); c.flushLoop(ctx) }()
	}
	go func() { defer wg.Done(); c.liveLoop(ctx) }()
	go func() { defer wg.Done(); c.maintenanceLoop(ctx) }()

	buf := make([]byte, 65535)
	for {
		n, from, err := conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			log.Printf("netflow: read: %v", err)
			continue
		}
		c.packets.Add(1)
		exp := from.Addr().Unmap().String()
		if !c.exporterAllowed(from.Addr().Unmap()) {
			c.refused.Add(1)
			continue
		}
		c.noteExporter(exp)
		flows, err := c.dec.Decode(exp, buf[:n])
		if err != nil {
			c.dropped.Add(1)
		}
		if len(flows) > 0 {
			c.ingest(flows, time.Now())
		}
	}
	wg.Wait()
	if c.db != nil {
		c.flush(context.Background()) // final flush on shutdown
	}
	return nil
}

// exporterAllowed applies netflow.allowed_exporters (empty = accept any).
func (c *Collector) exporterAllowed(a netip.Addr) bool {
	if len(c.allowed) == 0 {
		return true
	}
	for _, p := range c.allowed {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// noteExporter remembers an exporter for the status page, bounded so spoofed
// source addresses can't grow it forever.
func (c *Collector) noteExporter(exp string) {
	now := time.Now().Unix()
	if _, ok := c.exporters.Load(exp); !ok && c.exporterCount.Load() >= maxExporterKeys {
		return
	}
	if _, loaded := c.exporters.Swap(exp, now); !loaded {
		c.exporterCount.Add(1)
	}
}

// servicePort guesses which side of a connection is the service: the lower port
// (clients use high ephemeral ports).
func servicePort(f *netflow.Flow) uint16 {
	if f.Proto != 6 && f.Proto != 17 && f.Proto != 132 {
		return 0
	}
	if f.SrcPort == 0 || (f.DstPort != 0 && f.DstPort < f.SrcPort) {
		return f.DstPort
	}
	return f.SrcPort
}

func (c *Collector) ingest(flows []netflow.Flow, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	nowSec := now.Unix()
	for i := range flows {
		f := &flows[i]
		c.flows.Add(1)
		srcNet, dstNet := c.network(f.Src), c.network(f.Dst)
		switch {
		case srcNet != nil && dstNet == nil: // upload to internet
			c.add(f, f.Src, srcNet, counters{tx: f.Bytes}, nowSec)
			c.addPeer(f, f.Src, f.Dst, 0, f.Bytes)
		case dstNet != nil && srcNet == nil: // download from internet
			c.add(f, f.Dst, dstNet, counters{rx: f.Bytes}, nowSec)
			c.addPeer(f, f.Dst, f.Src, f.Bytes, 0)
		case srcNet != nil && dstNet != nil: // local / inter-VLAN / VPN<->LAN
			c.add(f, f.Src, srcNet, counters{lanTx: f.Bytes}, nowSec)
			c.add(f, f.Dst, dstNet, counters{lanRx: f.Bytes}, nowSec)
		default:
			c.ignored.Add(1)
		}
	}
}

// add records a flow's bytes for a local device: live rate, today's counters
// and (with a database) per-minute history.
func (c *Collector) add(f *netflow.Flow, ip netip.Addr, n *config.Network, v counters, nowSec int64) {
	if _, ok := c.seen[ip]; !ok {
		if len(c.seen) >= maxDevices {
			c.limited.Add(1) // device table full: ignore new addresses
			return
		}
		c.pending[ip] = true
	}
	c.seen[ip] = nowSec
	c.state.AddLive(ip, live.Counters{Rx: v.rx, Tx: v.tx, LanRx: v.lanRx, LanTx: v.lanTx}, nowSec)
	c.state.AddHost(ip.String(), c.names[ip], n.Name, n.Kind, v.rx, v.tx, v.lanRx, v.lanTx)
	if c.db != nil {
		c.addHistory(f, ip, v)
	}
}

// addHistory spreads a flow's bytes over the minutes it was active.
func (c *Collector) addHistory(f *netflow.Flow, ip netip.Addr, v counters) {
	start, end := f.Start.Unix(), f.End.Unix()
	if end < start {
		end = start
	}
	first, last := start-start%60, end-end%60
	nb := uint64((last-first)/60 + 1)
	if nb > maxSpreadMins {
		first, nb = last-(maxSpreadMins-1)*60, maxSpreadMins
	}
	if len(c.hosts) > maxHistoryKeys {
		first, nb = last, 1 // under pressure, don't fan out
		if _, ok := c.hosts[hostKey{last, ip}]; !ok {
			c.limited.Add(1)
			return
		}
	}
	share := func(x uint64, i uint64) uint64 {
		// integer split; the last bucket takes the remainder
		if i == nb-1 {
			return x - (x/nb)*(nb-1)
		}
		return x / nb
	}
	for i := uint64(0); i < nb; i++ {
		k := hostKey{first + int64(i)*60, ip}
		b := c.hosts[k]
		if b == nil {
			b = &counters{}
			c.hosts[k] = b
		}
		b.rx += share(v.rx, i)
		b.tx += share(v.tx, i)
		b.lanRx += share(v.lanRx, i)
		b.lanTx += share(v.lanTx, i)
	}
}

func (c *Collector) addPeer(f *netflow.Flow, local, remote netip.Addr, rx, tx uint64) {
	if c.db == nil {
		return
	}
	ts := f.End.Unix()
	k := peerKey{ts - ts%3600, local, remote, f.Proto, servicePort(f)}
	p := c.peers[k]
	if p == nil {
		if len(c.peers) >= maxPeerKeys {
			c.limited.Add(1)
			return
		}
		p = &[2]uint64{}
		c.peers[k] = p
	}
	p[0] += rx
	p[1] += tx
}

func (c *Collector) flushLoop(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.flush(ctx)
		}
	}
}

func (c *Collector) flush(ctx context.Context) {
	c.mu.Lock()
	hosts, peers := c.hosts, c.peers
	c.hosts, c.peers = map[hostKey]*counters{}, map[peerKey]*[2]uint64{}
	var infos []store.HostInfo
	now := time.Now().Unix()
	for ip := range c.pending {
		h := store.HostInfo{IP: ip.String(), Name: c.names[ip], Seen: c.seen[ip]}
		if n := c.network(ip); n != nil {
			h.Network, h.Kind = n.Name, n.Kind
		}
		infos = append(infos, h)
	}
	c.pending = map[netip.Addr]bool{}
	// refresh last_seen for active hosts once per flush
	for ip, s := range c.seen {
		if now-s < 30 {
			h := store.HostInfo{IP: ip.String(), Name: c.names[ip], Seen: s}
			if n := c.network(ip); n != nil {
				h.Network, h.Kind = n.Name, n.Kind
			}
			infos = append(infos, h)
		}
	}
	c.mu.Unlock()

	hb := make([]store.HostBucket, 0, len(hosts))
	for k, v := range hosts {
		hb = append(hb, store.HostBucket{TS: k.ts, IP: k.ip.String(), Rx: v.rx, Tx: v.tx, LanRx: v.lanRx, LanTx: v.lanTx})
	}
	pb := make([]store.PeerBucket, 0, len(peers))
	for k, v := range peers {
		pb = append(pb, store.PeerBucket{TS: k.ts, IP: k.ip.String(), Remote: k.remote.String(), Proto: k.proto, Port: k.port, Rx: v[0], Tx: v[1]})
	}
	if err := c.db.WriteTraffic(ctx, hb, pb); err != nil {
		log.Printf("db: write traffic: %v (re-queueing)", err)
		c.mu.Lock()
		for k, v := range hosts {
			b := c.hosts[k]
			if b == nil {
				c.hosts[k] = v
				continue
			}
			b.rx, b.tx, b.lanRx, b.lanTx = b.rx+v.rx, b.tx+v.tx, b.lanRx+v.lanRx, b.lanTx+v.lanTx
		}
		for k, v := range peers {
			if p := c.peers[k]; p != nil {
				p[0], p[1] = p[0]+v[0], p[1]+v[1]
			} else {
				c.peers[k] = v
			}
		}
		c.mu.Unlock()
		return
	}
	if err := c.db.UpsertHosts(ctx, infos); err != nil {
		log.Printf("db: hosts: %v", err)
	}
}

func (c *Collector) liveLoop(ctx context.Context) {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now := time.Now().Unix()
		win := int64(c.cfg.NetFlow.LiveWindow)
		bps := func(b uint64) float64 { return float64(b) * 8 / float64(win) }
		window := c.state.Window(now, win)
		var rows []store.LiveHost
		var mem []store.LiveHostRow
		c.mu.Lock()
		for ip, sum := range window {
			lh := store.LiveHost{IP: ip.String(), RxBps: bps(sum.Rx), TxBps: bps(sum.Tx), LanRxBps: bps(sum.LanRx), LanTxBps: bps(sum.LanTx)}
			rows = append(rows, lh)
			row := store.LiveHostRow{HostTotal: store.HostTotal{IP: lh.IP, Name: c.names[ip]}, RxBps: lh.RxBps, TxBps: lh.TxBps, LanRxBps: lh.LanRxBps, LanTxBps: lh.LanTxBps}
			if n := c.network(ip); n != nil {
				row.Network, row.Kind = n.Name, n.Kind
			}
			mem = append(mem, row)
		}
		c.mu.Unlock()
		c.state.SetHosts(mem)
		c.state.SetMeta(c.statusMeta())
		if c.db != nil {
			if err := c.db.SetLiveHosts(ctx, rows); err != nil && ctx.Err() == nil {
				log.Printf("db: live hosts: %v", err)
			}
		}
	}
}

// maintenanceLoop resolves hostnames, publishes collector status and prunes
// old data.
func (c *Collector) maintenanceLoop(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	lastPrune := time.Time{}
	for first := true; ; first = false {
		if !first {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
		if c.cfg.ReverseDNS {
			c.resolveNames(ctx)
		}
		meta := c.statusMeta()
		c.state.SetMeta(meta)
		if c.db != nil {
			if err := c.db.SetMeta(ctx, meta); err != nil && ctx.Err() == nil {
				log.Printf("db: meta: %v", err)
			}
		}
		c.forgetIdle(24 * time.Hour)
		if c.db != nil && time.Since(lastPrune) > time.Hour {
			lastPrune = time.Now()
			r := c.cfg.Retention
			if err := c.db.Prune(ctx, r.MinuteDays, r.HourDays, r.PeerDays); err != nil {
				log.Printf("db: prune: %v", err)
			}
		}
	}
}

// statusMeta reports collector health; shown by the dashboard and /api/v1/status.
func (c *Collector) statusMeta() map[string]string {
	var exps []string
	c.exporters.Range(func(k, v any) bool {
		if time.Now().Unix()-v.(int64) < 300 {
			exps = append(exps, k.(string))
		} else {
			c.exporters.Delete(k)
			c.exporterCount.Add(-1)
		}
		return true
	})
	sort.Strings(exps)
	expStr := ""
	for i, e := range exps {
		if i > 0 {
			expStr += ", "
		}
		expStr += e
	}
	return map[string]string{
		"history_enabled":     strconv.FormatBool(c.db != nil),
		"collector_started":   strconv.FormatInt(c.state.Started().Unix(), 10),
		"collector_heartbeat": strconv.FormatInt(time.Now().Unix(), 10),
		"flows_total":         strconv.FormatUint(c.flows.Load(), 10),
		"packets_total":       strconv.FormatUint(c.packets.Load(), 10),
		"flows_ignored":       strconv.FormatUint(c.ignored.Load(), 10),
		"packets_bad":         strconv.FormatUint(c.dropped.Load(), 10),
		"packets_refused":     strconv.FormatUint(c.refused.Load(), 10),
		"flows_limited":       strconv.FormatUint(c.limited.Load(), 10),
		"exporters":           expStr,
		"live_window":         strconv.Itoa(c.cfg.NetFlow.LiveWindow),
		"snmp_enabled":        strconv.FormatBool(c.cfg.SNMP.Enabled),
	}
}

// forgetIdle drops in-memory state for hosts not seen for a while (keeps memory
// bounded with IPv6 privacy addresses) and lets their names be re-resolved.
func (c *Collector) forgetIdle(d time.Duration) {
	cut := time.Now().Add(-d).Unix()
	c.mu.Lock()
	defer c.mu.Unlock()
	for ip, s := range c.seen {
		if s < cut {
			delete(c.seen, ip)
			if _, static := c.cfg.Hosts[ip.String()]; !static {
				delete(c.names, ip)
			}
		}
	}
}

func (c *Collector) resolveNames(ctx context.Context) {
	c.mu.Lock()
	var todo []netip.Addr
	for ip := range c.seen {
		if _, ok := c.names[ip]; !ok {
			todo = append(todo, ip)
		}
	}
	c.mu.Unlock()
	if len(todo) > 50 {
		todo = todo[:50]
	}
	for _, ip := range todo {
		lctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		names, _ := net.DefaultResolver.LookupAddr(lctx, ip.String())
		cancel()
		name := ""
		if len(names) > 0 {
			name = trimDot(names[0])
		}
		c.mu.Lock()
		c.names[ip] = name // empty name is cached too, so we don't retry forever
		if name != "" {
			c.pending[ip] = true
		}
		c.mu.Unlock()
	}
}

func trimDot(s string) string {
	if len(s) > 0 && s[len(s)-1] == '.' {
		return s[:len(s)-1]
	}
	return s
}
