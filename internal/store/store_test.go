package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Every query runs against SQLite. Set TM_TEST_MYSQL (e.g.
// mysql://root:pw@127.0.0.1:3306/tm) or TM_TEST_POSTGRES to also run them
// against a server; its tables are dropped first.
func TestDrivers(t *testing.T) {
	dbs := map[string][2]string{"sqlite": {"sqlite", filepath.Join(t.TempDir(), "tm.db")}}
	if dsn := os.Getenv("TM_TEST_MYSQL"); dsn != "" {
		dbs["mysql"] = [2]string{"mysql", dsn}
	}
	if dsn := os.Getenv("TM_TEST_POSTGRES"); dsn != "" {
		dbs["postgres"] = [2]string{"postgres", dsn}
	}
	for name, d := range dbs {
		t.Run(name, func(t *testing.T) { exercise(t, d[0], d[1]) })
	}
}

func exercise(t *testing.T, driver, dsn string) {
	ctx := context.Background()
	db, err := Open(driver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"traffic_minute", "traffic_hour", "peer_hour", "iface_minute", "iface_hour", "hosts", "ifaces", "networks", "live_hosts", "live_ifaces", "meta"} {
		if _, err := db.db.ExecContext(ctx, "DROP TABLE IF EXISTS "+table); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	// twice: creating the schema must be repeatable
	for range 2 {
		if db, err = Open(driver, dsn); err != nil {
			t.Fatal(err)
		}
		if err := db.migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	defer db.Close()

	now := time.Now().Truncate(time.Minute)
	ts := now.Add(-10 * time.Minute).Unix()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// counters add up across writes
	for range 2 {
		must(db.WriteTraffic(ctx,
			[]HostBucket{{TS: ts, IP: "192.168.1.200", Rx: 1000, Tx: 100, LanRx: 10, LanTx: 1}, {TS: ts, IP: "192.168.1.5", Rx: 5, Tx: 5}},
			[]PeerBucket{{TS: ts - ts%3600, IP: "192.168.1.200", Remote: "203.0.113.9", Proto: 6, Port: 443, Rx: 900, Tx: 90}}))
		must(db.WriteIfaces(ctx, []IfaceBucket{{TS: ts, Name: "igb0", In: 3000, Out: 300}}))
	}
	must(db.UpsertHosts(ctx, []HostInfo{{IP: "192.168.1.200", Name: "nas", Network: "LAN", Kind: "lan", Seen: ts}}))
	must(db.UpsertHosts(ctx, []HostInfo{{IP: "192.168.1.200", Name: "", Network: "LAN", Kind: "lan", Seen: ts + 60}})) // empty name keeps "nas"
	must(db.UpsertIfaces(ctx, []IfaceInfo{{Name: "igb0", Label: "WAN", Kind: "wan", Speed: 1e9}}))
	must(db.UpsertIfaces(ctx, []IfaceInfo{{Name: "igb0", Label: "WAN", Kind: "wan", Speed: 2e9}}))
	must(db.SyncNetworks(ctx, []Network{{Name: "LAN", Kind: "lan", CIDRs: "192.168.1.0/24"}}))
	must(db.SetLiveHosts(ctx, []LiveHost{{IP: "192.168.1.200", RxBps: 20e6, TxBps: 20e6}}))
	must(db.SetLiveIfaces(ctx, []LiveIface{{Name: "igb0", InBps: 1e6, OutBps: 2e6}}))
	must(db.SetMeta(ctx, map[string]string{"a": "1"}))
	must(db.SetMeta(ctx, map[string]string{"a": "2"}))
	must(db.RenameHost(ctx, "192.168.1.200", "Big NAS"))
	must(db.RenameIface(ctx, "igb0", "Internet"))

	meta, err := db.Meta(ctx)
	must(err)
	if meta["a"] != "2" {
		t.Errorf("meta a = %q", meta["a"])
	}
	hosts, err := db.Hosts(ctx)
	must(err)
	if h := hosts["192.168.1.200"]; h.Name != "nas" || h.Display() != "Big NAS" || h.LastSeen != ts+60 {
		t.Errorf("host = %+v", h)
	}
	nets, err := db.Networks(ctx)
	must(err)
	if len(nets) != 1 || nets[0].CIDRs != "192.168.1.0/24" {
		t.Errorf("networks = %+v", nets)
	}
	lh, li, err := db.Live(ctx, time.Minute)
	must(err)
	if len(lh) != 1 || lh[0].RxBps != 20e6 || lh[0].Name != "Big NAS" || len(li) != 1 || li[0].Label != "Internet" || li[0].Speed != 2e9 {
		t.Errorf("live = %+v %+v", lh, li)
	}

	from, to := now.Add(-time.Hour), now.Add(time.Minute)
	top, err := db.TopHosts(ctx, from, to, Filter{}, 10)
	must(err)
	if len(top) != 2 || top[0].IP != "192.168.1.200" || top[0].Rx != 2000 || top[0].LanTx != 2 {
		t.Errorf("top = %+v", top)
	}
	if top, err = db.TopHosts(ctx, from, to, Filter{Network: "LAN"}, 10); err != nil || len(top) != 1 {
		t.Errorf("top LAN = %+v, %v", top, err)
	}
	if top, err = db.TopHosts(ctx, now.Add(-30*24*time.Hour), to, Filter{IP: "192.168.1.200"}, 10); err != nil || len(top) != 1 || top[0].Tx != 200 {
		t.Errorf("top hourly = %+v, %v", top, err)
	}
	for _, step := range []string{"minute", "5min", "15min", "hour", "day", "week", "month"} {
		pts, _, err := db.Series(ctx, from, to, step, Filter{})
		must(err)
		var rx int64
		for _, p := range pts {
			rx += p.Rx
		}
		if rx != 2010 {
			t.Errorf("series %s: rx total %d want 2010", step, rx)
		}
	}
	pts, _, err := db.IfaceSeries(ctx, from, to, "minute", "igb0")
	must(err)
	var in int64
	for _, p := range pts {
		in += p.Rx
	}
	if in != 6000 {
		t.Errorf("iface series in = %d", in)
	}
	peers, err := db.Peers(ctx, from, to, Filter{IP: "192.168.1.200"}, 10)
	must(err)
	if len(peers) != 1 || peers[0].Rx != 1800 || peers[0].Port != 443 {
		t.Errorf("peers = %+v", peers)
	}
	totals, err := db.IfaceTotals(ctx, from, to)
	must(err)
	if len(totals) != 1 || totals[0].In != 6000 || totals[0].Label != "Internet" {
		t.Errorf("iface totals = %+v", totals)
	}
	must(db.Prune(ctx, 1, 365, 30))
}

func TestMySQLDSN(t *testing.T) {
	cases := map[string]string{
		"mysql://tm:p%40ss@db.lan/traffic":             "tm:p@ss@tcp(db.lan:3306)/traffic",
		"mysql://tm:pw@10.0.0.5:3307/traffic?tls=true": "tm:pw@tcp(10.0.0.5:3307)/traffic?tls=true",
		"tm:pw@tcp(db:3306)/traffic":                   "tm:pw@tcp(db:3306)/traffic",
	}
	for in, want := range cases {
		got, err := mysqlDSN(in)
		if err != nil || got != want {
			t.Errorf("mysqlDSN(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"mysql://tm:pw@db/", "not a dsn"} {
		if _, err := mysqlDSN(bad); err == nil {
			t.Errorf("mysqlDSN(%q) should fail", bad)
		}
	}
}
