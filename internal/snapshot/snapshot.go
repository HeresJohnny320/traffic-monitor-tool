// Package snapshot assembles the current state of the network (live rates,
// today's totals, collector status) from collector memory and/or the database.
// It backs the dashboard, the /api/v1 API, /metrics and MQTT.
package snapshot

import (
	"context"
	"math"
	"sort"
	"strconv"
	"sync"
	"time"

	"trafficmonitor/internal/live"
	"trafficmonitor/internal/store"
)

// Source reads from the collector's in-memory state (when running in the same
// process) and/or the database. At least one must be non-nil.
type Source struct {
	db   *store.DB
	live *live.State

	namesMu sync.Mutex
	names   map[string]store.Host
	namesAt time.Time
}

func New(db *store.DB, st *live.State) *Source { return &Source{db: db, live: st} }

// HistoryEnabled reports whether a database is available.
func (s *Source) HistoryEnabled() bool { return s.db != nil }

// This file is the one place that decides where data comes from:
//   - live rates: collector memory when running in the same process
//     (`traffic-monitor all`), otherwise the live_* tables written by the collector
//   - today's totals: the database when there is one (survives restarts),
//     otherwise collector memory (since midnight or since startup)
//   - history: database only

func CollectorAlive(meta map[string]string) bool {
	hb, _ := strconv.ParseInt(meta["collector_heartbeat"], 10, 64)
	return time.Now().Unix()-hb < 120
}

func (s *Source) Status(ctx context.Context) (map[string]string, []store.Network, error) {
	if s.live != nil {
		return s.live.Meta(), s.live.Networks(), nil
	}
	meta, err := s.db.Meta(ctx)
	if err != nil {
		return nil, nil, err
	}
	nets, err := s.db.Networks(ctx)
	return meta, nets, err
}

// hostNames returns device metadata from the database (including names set in
// the UI), cached briefly. Empty in live-only mode.
func (s *Source) HostNames(ctx context.Context) map[string]store.Host {
	if s.db == nil {
		return nil
	}
	s.namesMu.Lock()
	defer s.namesMu.Unlock()
	if s.names == nil || time.Since(s.namesAt) > 15*time.Second {
		if m, err := s.db.Hosts(ctx); err == nil {
			s.names, s.namesAt = m, time.Now()
		}
	}
	return s.names
}

func (s *Source) Live(ctx context.Context) ([]store.LiveHostRow, []store.LiveIfaceRow, error) {
	var hosts []store.LiveHostRow
	var ifs []store.LiveIfaceRow
	if s.live != nil {
		hosts, ifs = s.live.Live()
		names := s.HostNames(ctx)
		for i := range hosts {
			if h, ok := names[hosts[i].IP]; ok && h.Display() != "" {
				hosts[i].Name = h.Display()
			}
		}
		if s.db != nil {
			// interface labels renamed in the UI live in the database
			if m, err := s.db.Ifaces(ctx); err == nil {
				for i := range ifs {
					if f, ok := m[ifs[i].Name]; ok && f.Label != "" {
						ifs[i].Label = f.Label
					}
				}
			}
		}
	} else {
		var err error
		if hosts, ifs, err = s.db.Live(ctx, 30*time.Second); err != nil {
			return nil, nil, err
		}
	}
	for i := range ifs {
		if ifs[i].Label == "" {
			ifs[i].Label = ifs[i].Name
		}
	}
	if hosts == nil {
		hosts = []store.LiveHostRow{}
	}
	if ifs == nil {
		ifs = []store.LiveIfaceRow{}
	}
	return hosts, ifs, nil
}

func Midnight(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.Local)
}

// todayData returns per-device and per-interface totals for today and the
// time they are counted from.
func (s *Source) Today(ctx context.Context) ([]store.HostTotal, []store.IfaceTotal, time.Time, error) {
	now := time.Now()
	if s.db != nil {
		from, to := Midnight(now), now.Add(time.Minute)
		hosts, err := s.db.TopHosts(ctx, from, to, store.Filter{}, 100000)
		if err != nil {
			return nil, nil, from, err
		}
		ifs, err := s.db.IfaceTotals(ctx, from, to)
		return hosts, ifs, from, err
	}
	hosts, ifs, since := s.live.Today()
	return hosts, ifs, since, nil
}

type Device struct {
	IP                 string  `json:"ip"`
	Name               string  `json:"name"`
	Network            string  `json:"network"`
	Kind               string  `json:"kind"`
	Active             bool    `json:"active"`
	DownloadBps        float64 `json:"download_bps"`
	UploadBps          float64 `json:"upload_bps"`
	LocalBps           float64 `json:"local_bps"`
	TodayDownloadBytes int64   `json:"today_download_bytes"`
	TodayUploadBytes   int64   `json:"today_upload_bytes"`
	TodayLocalBytes    int64   `json:"today_local_bytes"`
}

type Iface struct {
	Name          string  `json:"name"`
	Label         string  `json:"label"`
	Kind          string  `json:"kind"`
	SpeedBps      int64   `json:"speed_bps"`
	InBps         float64 `json:"in_bps"`
	OutBps        float64 `json:"out_bps"`
	TodayInBytes  int64   `json:"today_in_bytes"`
	TodayOutBytes int64   `json:"today_out_bytes"`
}

type Network struct {
	Name               string  `json:"name"`
	Kind               string  `json:"kind"`
	CIDRs              string  `json:"cidrs"`
	ActiveDevices      int     `json:"active_devices"`
	DownloadBps        float64 `json:"download_bps"`
	UploadBps          float64 `json:"upload_bps"`
	LocalBps           float64 `json:"local_bps"`
	TodayDownloadBytes int64   `json:"today_download_bytes"`
	TodayUploadBytes   int64   `json:"today_upload_bytes"`
	TodayLocalBytes    int64   `json:"today_local_bytes"`
}

type WAN struct {
	DownloadBps        float64  `json:"download_bps"`
	UploadBps          float64  `json:"upload_bps"`
	DownloadMbps       float64  `json:"download_mbps"`
	UploadMbps         float64  `json:"upload_mbps"`
	TodayDownloadBytes int64    `json:"today_download_bytes"`
	TodayUploadBytes   int64    `json:"today_upload_bytes"`
	TodayDownloadGB    float64  `json:"today_download_gb"`
	TodayUploadGB      float64  `json:"today_upload_gb"`
	Source             string   `json:"source"` // snmp | netflow
	Interfaces         []string `json:"interfaces,omitempty"`
}

type Snapshot struct {
	Time     time.Time
	Since    time.Time
	Meta     map[string]string
	Devices  []Device
	Ifaces   []Iface
	Networks []Network
	WAN      WAN
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func (s *Source) Take(ctx context.Context) (*Snapshot, error) {
	meta, nets, err := s.Status(ctx)
	if err != nil {
		return nil, err
	}
	liveHosts, liveIfs, err := s.Live(ctx)
	if err != nil {
		return nil, err
	}
	todayHosts, todayIfs, since, err := s.Today(ctx)
	if err != nil {
		return nil, err
	}
	snap := &Snapshot{Time: time.Now(), Since: since, Meta: meta}

	// devices: union of live and today
	names := s.HostNames(ctx)
	byIP := map[string]*Device{}
	get := func(t store.HostTotal) *Device {
		d := byIP[t.IP]
		if d == nil {
			d = &Device{IP: t.IP}
			byIP[t.IP] = d
		}
		if t.Name != "" {
			d.Name = t.Name
		}
		if t.Network != "" {
			d.Network, d.Kind = t.Network, t.Kind
		}
		return d
	}
	for _, t := range todayHosts {
		d := get(t)
		d.TodayDownloadBytes, d.TodayUploadBytes, d.TodayLocalBytes = t.Rx, t.Tx, t.LanRx+t.LanTx
	}
	for _, l := range liveHosts {
		d := get(l.HostTotal)
		d.DownloadBps, d.UploadBps, d.LocalBps = l.RxBps, l.TxBps, l.LanRxBps+l.LanTxBps
		d.Active = d.DownloadBps+d.UploadBps+d.LocalBps > 0
	}
	for ip, d := range byIP {
		if h, ok := names[ip]; ok && h.Display() != "" {
			d.Name = h.Display()
		}
		snap.Devices = append(snap.Devices, *d)
	}
	sort.Slice(snap.Devices, func(i, j int) bool {
		a, b := snap.Devices[i], snap.Devices[j]
		if ra, rb := a.DownloadBps+a.UploadBps, b.DownloadBps+b.UploadBps; ra != rb {
			return ra > rb
		}
		return a.TodayDownloadBytes+a.TodayUploadBytes > b.TodayDownloadBytes+b.TodayUploadBytes
	})

	// interfaces: union of live and today
	byIf := map[string]*Iface{}
	for _, l := range liveIfs {
		byIf[l.Name] = &Iface{Name: l.Name, Label: l.Label, Kind: l.Kind, SpeedBps: l.Speed, InBps: l.InBps, OutBps: l.OutBps}
	}
	for _, t := range todayIfs {
		f := byIf[t.Name]
		if f == nil {
			f = &Iface{Name: t.Name, Label: t.Label, Kind: t.Kind}
			byIf[t.Name] = f
		}
		f.TodayInBytes, f.TodayOutBytes = t.In, t.Out
	}
	for _, f := range byIf {
		if f.Label == "" {
			f.Label = f.Name
		}
		snap.Ifaces = append(snap.Ifaces, *f)
	}
	sort.Slice(snap.Ifaces, func(i, j int) bool {
		if ki, kj := kindRank(snap.Ifaces[i].Kind), kindRank(snap.Ifaces[j].Kind); ki != kj {
			return ki < kj
		}
		return snap.Ifaces[i].Label < snap.Ifaces[j].Label
	})

	// networks
	idx := map[string]int{}
	for _, n := range nets {
		idx[n.Name] = len(snap.Networks)
		snap.Networks = append(snap.Networks, Network{Name: n.Name, Kind: n.Kind, CIDRs: n.CIDRs})
	}
	for _, d := range snap.Devices {
		i, ok := idx[d.Network]
		if !ok {
			continue
		}
		n := &snap.Networks[i]
		if d.Active {
			n.ActiveDevices++
		}
		n.DownloadBps += d.DownloadBps
		n.UploadBps += d.UploadBps
		n.LocalBps += d.LocalBps
		n.TodayDownloadBytes += d.TodayDownloadBytes
		n.TodayUploadBytes += d.TodayUploadBytes
		n.TodayLocalBytes += d.TodayLocalBytes
	}

	// WAN: SNMP counters of kind=wan interfaces, else the sum of all devices
	w := &snap.WAN
	for _, f := range snap.Ifaces {
		if f.Kind == "wan" {
			w.Source = "snmp"
			w.Interfaces = append(w.Interfaces, f.Name)
			w.DownloadBps += f.InBps
			w.UploadBps += f.OutBps
			w.TodayDownloadBytes += f.TodayInBytes
			w.TodayUploadBytes += f.TodayOutBytes
		}
	}
	if w.Source == "" {
		w.Source = "netflow"
		for _, d := range snap.Devices {
			w.DownloadBps += d.DownloadBps
			w.UploadBps += d.UploadBps
			w.TodayDownloadBytes += d.TodayDownloadBytes
			w.TodayUploadBytes += d.TodayUploadBytes
		}
	}
	w.DownloadMbps, w.UploadMbps = round2(w.DownloadBps/1e6), round2(w.UploadBps/1e6)
	w.TodayDownloadGB, w.TodayUploadGB = round2(float64(w.TodayDownloadBytes)/1e9), round2(float64(w.TodayUploadBytes)/1e9)
	return snap, nil
}

func kindRank(k string) int {
	switch k {
	case "wan":
		return 0
	case "lan":
		return 1
	case "vlan":
		return 2
	case "vpn":
		return 3
	}
	return 4
}

func (s *Source) Mode() string {
	if s.db == nil {
		return "live_only"
	}
	return "full"
}

// ActiveDevices counts devices with traffic in the live window.
func (s *Snapshot) ActiveDevices() int {
	n := 0
	for _, d := range s.Devices {
		if d.Active {
			n++
		}
	}
	return n
}
