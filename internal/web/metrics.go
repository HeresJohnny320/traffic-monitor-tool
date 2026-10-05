package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"trafficmonitor/internal/snapshot"
)

// metrics serves the live snapshot in Prometheus text format. Rates are
// gauges in bits/second; "today" values are gauges that reset at midnight.
func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	snap, err := s.src.Take(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var b strings.Builder
	family := func(name, typ, help string) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
	}
	sample := func(name string, v float64, labels ...string) {
		b.WriteString(name)
		if len(labels) > 0 {
			b.WriteByte('{')
			for i := 0; i+1 < len(labels); i += 2 {
				if i > 0 {
					b.WriteByte(',')
				}
				b.WriteString(labels[i])
				b.WriteString(`="`)
				b.WriteString(escapeLabel(labels[i+1]))
				b.WriteByte('"')
			}
			b.WriteByte('}')
		}
		b.WriteByte(' ')
		b.WriteString(strconv.FormatFloat(v, 'g', -1, 64))
		b.WriteByte('\n')
	}
	bool01 := func(v bool) float64 {
		if v {
			return 1
		}
		return 0
	}

	family("traffic_monitor_collector_up", "gauge", "1 if the collector sent a heartbeat in the last 2 minutes.")
	sample("traffic_monitor_collector_up", bool01(snapshot.CollectorAlive(snap.Meta)))
	family("traffic_monitor_history_enabled", "gauge", "1 if history is stored in a database, 0 in live-only mode.")
	sample("traffic_monitor_history_enabled", bool01(s.db != nil))
	family("traffic_monitor_flows_total", "counter", "NetFlow records received since the collector started.")
	f, _ := strconv.ParseFloat(snap.Meta["flows_total"], 64)
	sample("traffic_monitor_flows_total", f)

	family("traffic_monitor_wan_download_bits_per_second", "gauge", "Internet download rate.")
	sample("traffic_monitor_wan_download_bits_per_second", snap.WAN.DownloadBps, "source", snap.WAN.Source)
	family("traffic_monitor_wan_upload_bits_per_second", "gauge", "Internet upload rate.")
	sample("traffic_monitor_wan_upload_bits_per_second", snap.WAN.UploadBps, "source", snap.WAN.Source)
	family("traffic_monitor_wan_today_download_bytes", "gauge", "Bytes downloaded from the internet since midnight.")
	sample("traffic_monitor_wan_today_download_bytes", float64(snap.WAN.TodayDownloadBytes), "source", snap.WAN.Source)
	family("traffic_monitor_wan_today_upload_bytes", "gauge", "Bytes uploaded to the internet since midnight.")
	sample("traffic_monitor_wan_today_upload_bytes", float64(snap.WAN.TodayUploadBytes), "source", snap.WAN.Source)

	family("traffic_monitor_active_devices", "gauge", "Devices with traffic in the live window.")
	sample("traffic_monitor_active_devices", float64(snap.ActiveDevices()))

	devFam := []struct {
		name, help string
		val        func(snapshot.Device) float64
	}{
		{"traffic_monitor_device_download_bits_per_second", "Device internet download rate.", func(d snapshot.Device) float64 { return d.DownloadBps }},
		{"traffic_monitor_device_upload_bits_per_second", "Device internet upload rate.", func(d snapshot.Device) float64 { return d.UploadBps }},
		{"traffic_monitor_device_local_bits_per_second", "Device local (LAN/inter-VLAN/VPN) rate.", func(d snapshot.Device) float64 { return d.LocalBps }},
		{"traffic_monitor_device_today_download_bytes", "Device internet bytes downloaded since midnight.", func(d snapshot.Device) float64 { return float64(d.TodayDownloadBytes) }},
		{"traffic_monitor_device_today_upload_bytes", "Device internet bytes uploaded since midnight.", func(d snapshot.Device) float64 { return float64(d.TodayUploadBytes) }},
	}
	for _, fam := range devFam {
		family(fam.name, "gauge", fam.help)
		for _, d := range snap.Devices {
			sample(fam.name, fam.val(d), "ip", d.IP, "name", d.Name, "network", d.Network)
		}
	}

	ifFam := []struct {
		name, help string
		val        func(snapshot.Iface) float64
	}{
		{"traffic_monitor_interface_in_bits_per_second", "Interface receive rate (firewall's view).", func(f snapshot.Iface) float64 { return f.InBps }},
		{"traffic_monitor_interface_out_bits_per_second", "Interface transmit rate (firewall's view).", func(f snapshot.Iface) float64 { return f.OutBps }},
		{"traffic_monitor_interface_today_in_bytes", "Interface bytes received since midnight.", func(f snapshot.Iface) float64 { return float64(f.TodayInBytes) }},
		{"traffic_monitor_interface_today_out_bytes", "Interface bytes transmitted since midnight.", func(f snapshot.Iface) float64 { return float64(f.TodayOutBytes) }},
	}
	for _, fam := range ifFam {
		family(fam.name, "gauge", fam.help)
		for _, f := range snap.Ifaces {
			sample(fam.name, fam.val(f), "interface", f.Name, "label", f.Label, "kind", f.Kind)
		}
	}

	netFam := []struct {
		name, help string
		val        func(snapshot.Network) float64
	}{
		{"traffic_monitor_network_download_bits_per_second", "Network internet download rate.", func(n snapshot.Network) float64 { return n.DownloadBps }},
		{"traffic_monitor_network_upload_bits_per_second", "Network internet upload rate.", func(n snapshot.Network) float64 { return n.UploadBps }},
		{"traffic_monitor_network_active_devices", "Active devices in the network.", func(n snapshot.Network) float64 { return float64(n.ActiveDevices) }},
		{"traffic_monitor_network_today_download_bytes", "Network internet bytes downloaded since midnight.", func(n snapshot.Network) float64 { return float64(n.TodayDownloadBytes) }},
		{"traffic_monitor_network_today_upload_bytes", "Network internet bytes uploaded since midnight.", func(n snapshot.Network) float64 { return float64(n.TodayUploadBytes) }},
	}
	for _, fam := range netFam {
		family(fam.name, "gauge", fam.help)
		for _, n := range snap.Networks {
			sample(fam.name, fam.val(n), "network", n.Name, "kind", n.Kind)
		}
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Write([]byte(b.String()))
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

func escapeLabel(s string) string { return labelEscaper.Replace(s) }
