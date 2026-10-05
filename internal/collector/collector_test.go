package collector

import (
	"net/netip"
	"testing"
	"time"

	"trafficmonitor/internal/config"
	"trafficmonitor/internal/live"
	"trafficmonitor/internal/netflow"
)

func testCollector(t *testing.T, yaml string) *Collector {
	t.Helper()
	cfg, err := config.Parse([]byte("networks: [{name: LAN, cidr: [10.0.0.0/8]}]\n" + yaml))
	if err != nil {
		t.Fatal(err)
	}
	return New(cfg, nil, live.New())
}

// A flood of spoofed flows with random local addresses must not grow memory
// without bound.
func TestDeviceCap(t *testing.T) {
	c := testCollector(t, "")
	now := time.Now()
	var flows []netflow.Flow
	for i := 0; i < maxDevices+5000; i++ {
		ip := netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)})
		flows = append(flows, netflow.Flow{Src: netip.MustParseAddr("8.8.8.8"), Dst: ip, Bytes: 100, Start: now, End: now})
	}
	c.ingest(flows, now)
	if len(c.seen) > maxDevices {
		t.Fatalf("tracked %d devices, cap is %d", len(c.seen), maxDevices)
	}
	if c.limited.Load() != 5000 {
		t.Fatalf("expected 5000 limited flows, got %d", c.limited.Load())
	}
	if hs, _, _ := c.state.Today(); len(hs) > maxDevices {
		t.Fatalf("today's counters grew to %d", len(hs))
	}
}

func TestAllowedExporters(t *testing.T) {
	c := testCollector(t, "netflow: {allowed_exporters: [192.168.1.1, 10.10.0.0/16]}")
	for ip, want := range map[string]bool{"192.168.1.1": true, "10.10.5.5": true, "192.168.1.2": false, "::ffff:192.168.1.1": true} {
		if got := c.exporterAllowed(netip.MustParseAddr(ip).Unmap()); got != want {
			t.Errorf("%s: got %v want %v", ip, got, want)
		}
	}
	if open := testCollector(t, ""); !open.exporterAllowed(netip.MustParseAddr("1.2.3.4")) {
		t.Error("empty allowlist should accept any exporter")
	}
}

// Live rates are kept in the shared state, so they survive a settings reload
// (which replaces the collector).
func TestLiveRatesSurviveReload(t *testing.T) {
	st := live.New()
	cfg, _ := config.Parse([]byte("networks: [{name: LAN, cidr: [10.0.0.0/8]}]"))
	now := time.Now()
	New(cfg, nil, st).ingest([]netflow.Flow{{Src: netip.MustParseAddr("1.1.1.1"), Dst: netip.MustParseAddr("10.0.0.5"), Bytes: 6000, Start: now, End: now}}, now)
	_ = New(cfg, nil, st) // reload: new collector, same state
	if w := st.Window(now.Unix(), 60); w[netip.MustParseAddr("10.0.0.5")].Rx != 6000 {
		t.Fatalf("live window lost on reload: %+v", w)
	}
}
