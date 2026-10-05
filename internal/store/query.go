package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Network struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	CIDRs string `json:"cidrs"`
}

type Host struct {
	IP         string `json:"ip"`
	Name       string `json:"name"`
	CustomName string `json:"custom_name"`
	Network    string `json:"network"`
	Kind       string `json:"kind"`
	FirstSeen  int64  `json:"first_seen"`
	LastSeen   int64  `json:"last_seen"`
}

// Display returns the best human name for a host.
func (h Host) Display() string {
	if h.CustomName != "" {
		return h.CustomName
	}
	return h.Name
}

type Iface struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	CustomLabel string `json:"custom_label"`
	Kind        string `json:"kind"`
	Speed       int64  `json:"speed"`
	LastSeen    int64  `json:"last_seen"`
}

type HostTotal struct {
	IP      string `json:"ip"`
	Name    string `json:"name"`
	Network string `json:"network"`
	Kind    string `json:"kind"`
	Rx      int64  `json:"rx"`
	Tx      int64  `json:"tx"`
	LanRx   int64  `json:"lan_rx"`
	LanTx   int64  `json:"lan_tx"`
}

type IfaceTotal struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
	In    int64  `json:"in"`
	Out   int64  `json:"out"`
}

type Point struct {
	T     int64 `json:"t"`
	Rx    int64 `json:"rx"`
	Tx    int64 `json:"tx"`
	LanRx int64 `json:"lan_rx,omitempty"`
	LanTx int64 `json:"lan_tx,omitempty"`
}

type Peer struct {
	IP     string `json:"ip,omitempty"`
	Remote string `json:"remote"`
	Proto  int    `json:"proto"`
	Port   int    `json:"port"`
	Rx     int64  `json:"rx"`
	Tx     int64  `json:"tx"`
}

type LiveHostRow struct {
	HostTotal
	RxBps    float64 `json:"rx_bps"`
	TxBps    float64 `json:"tx_bps"`
	LanRxBps float64 `json:"lan_rx_bps"`
	LanTxBps float64 `json:"lan_tx_bps"`
}

type LiveIfaceRow struct {
	Name   string  `json:"name"`
	Label  string  `json:"label"`
	Kind   string  `json:"kind"`
	Speed  int64   `json:"speed"`
	InBps  float64 `json:"in_bps"`
	OutBps float64 `json:"out_bps"`
}

// ---------------------------------------------------------------- metadata

func (d *DB) Hosts(ctx context.Context) (map[string]Host, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT ip, name, custom_name, network, kind, first_seen, last_seen FROM hosts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]Host{}
	for rows.Next() {
		var h Host
		if err := rows.Scan(&h.IP, &h.Name, &h.CustomName, &h.Network, &h.Kind, &h.FirstSeen, &h.LastSeen); err != nil {
			return nil, err
		}
		m[h.IP] = h
	}
	return m, rows.Err()
}

func (d *DB) Ifaces(ctx context.Context) (map[string]Iface, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT name, label, custom_label, kind, speed, last_seen FROM ifaces`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]Iface{}
	for rows.Next() {
		var i Iface
		if err := rows.Scan(&i.Name, &i.Label, &i.CustomLabel, &i.Kind, &i.Speed, &i.LastSeen); err != nil {
			return nil, err
		}
		if i.CustomLabel != "" {
			i.Label = i.CustomLabel
		}
		m[i.Name] = i
	}
	return m, rows.Err()
}

func (d *DB) Networks(ctx context.Context) ([]Network, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT name, kind, cidrs FROM networks ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Network
	for rows.Next() {
		var n Network
		if err := rows.Scan(&n.Name, &n.Kind, &n.CIDRs); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (d *DB) Meta(ctx context.Context) (map[string]string, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT `+d.metaKey()+`, value FROM meta`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		m[k] = v
	}
	return m, rows.Err()
}

func (d *DB) RenameHost(ctx context.Context, ip, name string) error {
	res, err := d.db.ExecContext(ctx, d.q(`UPDATE hosts SET custom_name = ? WHERE ip = ?`), name, ip)
	return mustAffect(res, err)
}

func (d *DB) RenameIface(ctx context.Context, name, label string) error {
	res, err := d.db.ExecContext(ctx, d.q(`UPDATE ifaces SET custom_label = ? WHERE name = ?`), label, name)
	return mustAffect(res, err)
}

func mustAffect(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ---------------------------------------------------------------- live

func (d *DB) Live(ctx context.Context, maxAge time.Duration) ([]LiveHostRow, []LiveIfaceRow, error) {
	cut := time.Now().Add(-maxAge).Unix()
	hosts, err := d.Hosts(ctx)
	if err != nil {
		return nil, nil, err
	}
	rows, err := d.db.QueryContext(ctx, d.q(`SELECT ip, rx_bps, tx_bps, lan_rx_bps, lan_tx_bps FROM live_hosts WHERE updated >= ?`), cut)
	if err != nil {
		return nil, nil, err
	}
	var lh []LiveHostRow
	for rows.Next() {
		var r LiveHostRow
		if err := rows.Scan(&r.IP, &r.RxBps, &r.TxBps, &r.LanRxBps, &r.LanTxBps); err != nil {
			rows.Close()
			return nil, nil, err
		}
		fillHost(&r.HostTotal, hosts)
		lh = append(lh, r)
	}
	rows.Close()
	sort.Slice(lh, func(i, j int) bool { return lh[i].RxBps+lh[i].TxBps > lh[j].RxBps+lh[j].TxBps })

	ifs, err := d.Ifaces(ctx)
	if err != nil {
		return nil, nil, err
	}
	rows, err = d.db.QueryContext(ctx, d.q(`SELECT name, in_bps, out_bps FROM live_ifaces WHERE updated >= ?`), cut)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var li []LiveIfaceRow
	for rows.Next() {
		var r LiveIfaceRow
		if err := rows.Scan(&r.Name, &r.InBps, &r.OutBps); err != nil {
			return nil, nil, err
		}
		if m, ok := ifs[r.Name]; ok {
			r.Label, r.Kind, r.Speed = m.Label, m.Kind, m.Speed
		}
		if r.Label == "" {
			r.Label = r.Name
		}
		li = append(li, r)
	}
	SortIfaces(li)
	return lh, li, rows.Err()
}

// SortIfaces orders interfaces WAN, LAN, VLAN, VPN, other, then by label.
func SortIfaces(li []LiveIfaceRow) {
	sort.Slice(li, func(i, j int) bool {
		if kindOrder(li[i].Kind) != kindOrder(li[j].Kind) {
			return kindOrder(li[i].Kind) < kindOrder(li[j].Kind)
		}
		return li[i].Label < li[j].Label
	})
}

func kindOrder(k string) int {
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

func fillHost(t *HostTotal, hosts map[string]Host) {
	if h, ok := hosts[t.IP]; ok {
		t.Name, t.Network, t.Kind = h.Display(), h.Network, h.Kind
	}
}

// ---------------------------------------------------------------- history

// useMinute reports whether [from,to) can be answered from the per-minute
// table: short ranges whose start hasn't been pruned from it yet.
func (d *DB) useMinute(ctx context.Context, table string, from, to time.Time) bool {
	if to.Sub(from) > 72*time.Hour {
		return false
	}
	var minMinute, minHour sql.NullInt64
	d.db.QueryRowContext(ctx, `SELECT MIN(ts) FROM `+table).Scan(&minMinute)
	if !minMinute.Valid || from.Unix() >= minMinute.Int64 {
		return true
	}
	// from predates the minute data; that's fine as long as nothing was pruned
	// (the hourly table doesn't reach further back either)
	hourTable := strings.TrimSuffix(table, "_minute") + "_hour"
	d.db.QueryRowContext(ctx, `SELECT MIN(ts) FROM `+hourTable).Scan(&minHour)
	return !minHour.Valid || minHour.Int64 >= minMinute.Int64-minMinute.Int64%3600
}

func (d *DB) sums4() string {
	return d.sum("rx") + ", " + d.sum("tx") + ", " + d.sum("lan_rx") + ", " + d.sum("lan_tx")
}

// hourAlign widens from to the start of its hour when reading an hourly table.
func hourAlign(table string, from time.Time) time.Time {
	if strings.HasSuffix(table, "_hour") {
		return from.Truncate(time.Hour)
	}
	return from
}

type Filter struct {
	IP      string
	Network string
}

func (f Filter) where() (string, []any) {
	switch {
	case f.IP != "":
		return ` AND ip = ?`, []any{f.IP}
	case f.Network != "":
		return ` AND ip IN (SELECT ip FROM hosts WHERE network = ?)`, []any{f.Network}
	}
	return "", nil
}

// TopHosts returns per-host totals for [from,to), largest internet users first.
func (d *DB) TopHosts(ctx context.Context, from, to time.Time, f Filter, limit int) ([]HostTotal, error) {
	table := "traffic_hour"
	if d.useMinute(ctx, "traffic_minute", from, to) {
		table = "traffic_minute"
	}
	from = hourAlign(table, from)
	w, args := f.where()
	args = append([]any{from.Unix(), to.Unix()}, args...)
	args = append(args, limit)
	rows, err := d.db.QueryContext(ctx, d.q(`SELECT ip, `+d.sums4()+` FROM `+table+`
		WHERE ts >= ? AND ts < ?`+w+` GROUP BY ip ORDER BY SUM(rx) + SUM(tx) DESC, SUM(lan_rx) + SUM(lan_tx) DESC LIMIT ?`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	hosts, err := d.Hosts(ctx)
	if err != nil {
		return nil, err
	}
	var out []HostTotal
	for rows.Next() {
		var t HostTotal
		if err := rows.Scan(&t.IP, &t.Rx, &t.Tx, &t.LanRx, &t.LanTx); err != nil {
			return nil, err
		}
		fillHost(&t, hosts)
		out = append(out, t)
	}
	return out, rows.Err()
}

// Steps understood by Series.
var stepSeconds = map[string]int64{"minute": 60, "5min": 300, "15min": 900, "hour": 3600}

// Series returns traffic over time. step is one of minute, 5min, 15min, hour,
// day, week, month. The step actually used is returned (minute steps fall back
// to hour when minute data has been pruned).
func (d *DB) Series(ctx context.Context, from, to time.Time, step string, f Filter) ([]Point, string, error) {
	w, fargs := f.where()
	return d.series(ctx, "traffic", d.sums4(), w, fargs, from, to, step)
}

// IfaceSeries is Series for an interface: Rx = in, Tx = out.
func (d *DB) IfaceSeries(ctx context.Context, from, to time.Time, step, name string) ([]Point, string, error) {
	w, args := "", []any(nil)
	if name != "" {
		w, args = ` AND name = ?`, []any{name}
	}
	return d.series(ctx, "iface", d.sum("in_bytes")+", "+d.sum("out_bytes")+", 0, 0", w, args, from, to, step)
}

func (d *DB) series(ctx context.Context, prefix, cols, where string, wargs []any, from, to time.Time, step string) ([]Point, string, error) {
	sec, sub := stepSeconds[step]
	if _, ok := map[string]bool{"day": true, "week": true, "month": true}[step]; !ok && !sub {
		return nil, "", fmt.Errorf("bad step %q", step)
	}
	table := prefix + "_hour"
	if sub && sec < 3600 {
		if d.useMinute(ctx, prefix+"_minute", from, to) {
			table = prefix + "_minute"
		} else {
			step, sec = "hour", 3600
		}
	}
	qfrom := hourAlign(table, from)
	bucketSec := int64(3600)
	if sub {
		bucketSec = sec
	}
	args := append([]any{qfrom.Unix(), to.Unix()}, wargs...)
	bucket := "ts - ts % " + strconv.FormatInt(bucketSec, 10)
	rows, err := d.db.QueryContext(ctx, d.q(`SELECT `+bucket+` AS b, `+cols+` FROM `+table+`
		WHERE ts >= ? AND ts < ?`+where+` GROUP BY `+bucket+` ORDER BY 1`), args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	acc := map[int64]*Point{}
	for rows.Next() {
		var p Point
		if err := rows.Scan(&p.T, &p.Rx, &p.Tx, &p.LanRx, &p.LanTx); err != nil {
			return nil, "", err
		}
		k := bucketStart(time.Unix(p.T, 0), step).Unix()
		if a, ok := acc[k]; ok {
			a.Rx += p.Rx
			a.Tx += p.Tx
			a.LanRx += p.LanRx
			a.LanTx += p.LanTx
		} else {
			p.T = k
			acc[k] = &p
		}
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	// emit every bucket in range so charts have a continuous axis
	var out []Point
	for t := bucketStart(from, step); t.Before(to); t = nextBucket(t, step) {
		if p, ok := acc[t.Unix()]; ok {
			out = append(out, *p)
		} else {
			out = append(out, Point{T: t.Unix()})
		}
		if len(out) > 20000 {
			break
		}
	}
	return out, step, nil
}

func bucketStart(t time.Time, step string) time.Time {
	t = t.Local()
	switch step {
	case "day":
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
	case "week":
		wd := (int(t.Weekday()) + 6) % 7 // Monday = 0
		return time.Date(t.Year(), t.Month(), t.Day()-wd, 0, 0, 0, 0, time.Local)
	case "month":
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.Local)
	}
	s := stepSeconds[step]
	u := t.Unix()
	return time.Unix(u-u%s, 0)
}

func nextBucket(t time.Time, step string) time.Time {
	switch step {
	case "day":
		return t.AddDate(0, 0, 1)
	case "week":
		return t.AddDate(0, 0, 7)
	case "month":
		return t.AddDate(0, 1, 0)
	}
	return t.Add(time.Duration(stepSeconds[step]) * time.Second)
}

// Peers returns the remote endpoints a host (or, with ip == "", the whole
// network) exchanged the most internet traffic with.
func (d *DB) Peers(ctx context.Context, from, to time.Time, f Filter, limit int) ([]Peer, error) {
	w, wargs := f.where()
	args := append([]any{from.Unix() - from.Unix()%3600, to.Unix()}, wargs...)
	args = append(args, limit)
	rows, err := d.db.QueryContext(ctx, d.q(`SELECT remote, proto, port, `+d.sum("rx")+`, `+d.sum("tx")+` FROM peer_hour
		WHERE ts >= ? AND ts < ?`+w+` GROUP BY remote, proto, port ORDER BY SUM(rx) + SUM(tx) DESC LIMIT ?`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Peer
	for rows.Next() {
		var p Peer
		if err := rows.Scan(&p.Remote, &p.Proto, &p.Port, &p.Rx, &p.Tx); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// IfaceTotals returns in/out byte totals per interface for [from,to).
func (d *DB) IfaceTotals(ctx context.Context, from, to time.Time) ([]IfaceTotal, error) {
	table := "iface_hour"
	if d.useMinute(ctx, "iface_minute", from, to) {
		table = "iface_minute"
	}
	from = hourAlign(table, from)
	rows, err := d.db.QueryContext(ctx, d.q(`SELECT name, `+d.sum("in_bytes")+`, `+d.sum("out_bytes")+` FROM `+table+`
		WHERE ts >= ? AND ts < ? GROUP BY name`), from.Unix(), to.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ifs, err := d.Ifaces(ctx)
	if err != nil {
		return nil, err
	}
	var out []IfaceTotal
	for rows.Next() {
		var t IfaceTotal
		if err := rows.Scan(&t.Name, &t.In, &t.Out); err != nil {
			return nil, err
		}
		t.Label = t.Name
		if m, ok := ifs[t.Name]; ok {
			t.Label, t.Kind = m.Label, m.Kind
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if kindOrder(out[i].Kind) != kindOrder(out[j].Kind) {
			return kindOrder(out[i].Kind) < kindOrder(out[j].Kind)
		}
		return out[i].In+out[i].Out > out[j].In+out[j].Out
	})
	return out, rows.Err()
}
