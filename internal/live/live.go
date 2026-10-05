// Package live holds the collector's current view of the network in memory:
// per-device and per-interface rates plus counters since local midnight.
//
// It is always maintained by the collector, so the API works the same with or
// without a database (database.enabled: false = live-only mode).
package live

import (
	"maps"
	"net/netip"
	"sort"
	"sync"
	"time"

	"trafficmonitor/internal/store"
)

// Counters are byte counts: internet download/upload and local traffic.
type Counters struct{ Rx, Tx, LanRx, LanTx uint64 }

type slot struct {
	sec int64
	c   Counters
}

type State struct {
	mu       sync.RWMutex
	slots    map[netip.Addr][]slot // per-second bytes inside the live window
	started  time.Time
	hosts    []store.LiveHostRow
	ifaces   []store.LiveIfaceRow
	since    time.Time // start of the "today" counters
	today    map[string]*store.HostTotal
	todayIfs map[string]*store.IfaceTotal
	meta     map[string]string
	networks []store.Network
}

func New() *State {
	now := time.Now()
	return &State{
		started:  now,
		since:    now,
		today:    map[string]*store.HostTotal{},
		todayIfs: map[string]*store.IfaceTotal{},
		meta:     map[string]string{},
		slots:    map[netip.Addr][]slot{},
	}
}

// AddLive records bytes that arrived during second sec, for live rates. It
// lives here (not in the collector) so rates survive settings reloads.
func (s *State) AddLive(ip netip.Addr, c Counters, sec int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sl := s.slots[ip]
	if len(sl) == 0 || sl[len(sl)-1].sec != sec {
		sl = append(sl, slot{sec: sec})
	}
	x := &sl[len(sl)-1].c
	x.Rx += c.Rx
	x.Tx += c.Tx
	x.LanRx += c.LanRx
	x.LanTx += c.LanTx
	s.slots[ip] = sl
}

// Window sums each device's bytes over the last `window` seconds before now
// and drops anything older.
func (s *State) Window(now, window int64) map[netip.Addr]Counters {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[netip.Addr]Counters, len(s.slots))
	for ip, sl := range s.slots {
		i := 0
		for i < len(sl) && sl[i].sec <= now-window {
			i++
		}
		sl = sl[i:]
		if len(sl) == 0 {
			delete(s.slots, ip)
			continue
		}
		s.slots[ip] = sl
		var sum Counters
		for _, x := range sl {
			sum.Rx += x.c.Rx
			sum.Tx += x.c.Tx
			sum.LanRx += x.c.LanRx
			sum.LanTx += x.c.LanTx
		}
		out[ip] = sum
	}
	return out
}

func (s *State) Started() time.Time { return s.started }

// rollover resets the daily counters at local midnight. Caller holds s.mu.
func (s *State) rollover(now time.Time) {
	y1, m1, d1 := s.since.Date()
	y2, m2, d2 := now.Date()
	if y1 != y2 || m1 != m2 || d1 != d2 {
		s.since = time.Date(y2, m2, d2, 0, 0, 0, 0, time.Local)
		s.today = map[string]*store.HostTotal{}
		s.todayIfs = map[string]*store.IfaceTotal{}
	}
}

// AddHost adds bytes to a device's counters for today.
func (s *State) AddHost(ip, name, network, kind string, rx, tx, lanRx, lanTx uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rollover(time.Now())
	t := s.today[ip]
	if t == nil {
		t = &store.HostTotal{IP: ip}
		s.today[ip] = t
	}
	t.Name, t.Network, t.Kind = name, network, kind
	t.Rx += int64(rx)
	t.Tx += int64(tx)
	t.LanRx += int64(lanRx)
	t.LanTx += int64(lanTx)
}

// AddIface adds bytes to an interface's counters for today.
func (s *State) AddIface(name, label, kind string, in, out uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rollover(time.Now())
	t := s.todayIfs[name]
	if t == nil {
		t = &store.IfaceTotal{Name: name}
		s.todayIfs[name] = t
	}
	t.Label, t.Kind = label, kind
	t.In += int64(in)
	t.Out += int64(out)
}

func (s *State) SetHosts(rows []store.LiveHostRow) {
	sort.Slice(rows, func(i, j int) bool { return rows[i].RxBps+rows[i].TxBps > rows[j].RxBps+rows[j].TxBps })
	s.mu.Lock()
	s.hosts = rows
	s.mu.Unlock()
}

func (s *State) SetIfaces(rows []store.LiveIfaceRow) {
	s.mu.Lock()
	s.ifaces = rows
	s.mu.Unlock()
}

func (s *State) SetMeta(kv map[string]string) {
	s.mu.Lock()
	maps.Copy(s.meta, kv)
	s.mu.Unlock()
}

func (s *State) SetNetworks(n []store.Network) {
	s.mu.Lock()
	s.networks = n
	s.mu.Unlock()
}

// Live returns copies of the current device and interface rates.
func (s *State) Live() ([]store.LiveHostRow, []store.LiveIfaceRow) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]store.LiveHostRow(nil), s.hosts...), append([]store.LiveIfaceRow(nil), s.ifaces...)
}

// Today returns per-device and per-interface totals since local midnight (or
// since startup, if that was later).
func (s *State) Today() ([]store.HostTotal, []store.IfaceTotal, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rollover(time.Now())
	hs := make([]store.HostTotal, 0, len(s.today))
	for _, t := range s.today {
		hs = append(hs, *t)
	}
	sort.Slice(hs, func(i, j int) bool { return hs[i].Rx+hs[i].Tx > hs[j].Rx+hs[j].Tx })
	is := make([]store.IfaceTotal, 0, len(s.todayIfs))
	for _, t := range s.todayIfs {
		is = append(is, *t)
	}
	sort.Slice(is, func(i, j int) bool { return is[i].Name < is[j].Name })
	return hs, is, s.since
}

func (s *State) Meta() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return maps.Clone(s.meta)
}

func (s *State) Networks() []store.Network {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]store.Network(nil), s.networks...)
}
