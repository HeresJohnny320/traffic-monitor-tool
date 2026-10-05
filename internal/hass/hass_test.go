package hass

import (
	"context"
	"encoding/json"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	mochi "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"

	"trafficmonitor/internal/config"
	"trafficmonitor/internal/live"
	"trafficmonitor/internal/snapshot"
	"trafficmonitor/internal/store"
)

// End to end: embedded broker, publisher, and a subscriber playing Home Assistant.
func TestDiscoveryAndState(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	broker := mochi.New(&mochi.Options{InlineClient: true})
	broker.AddHook(new(auth.AllowHook), nil)
	if err := broker.AddListener(listeners.NewTCP(listeners.Config{ID: "t", Address: addr})); err != nil {
		t.Fatal(err)
	}
	go broker.Serve()
	defer broker.Close()

	st := live.New()
	st.SetNetworks([]store.Network{{Name: "IoT VLAN", Kind: "vlan", CIDRs: "192.168.20.0/24"}})
	st.SetMeta(map[string]string{"collector_heartbeat": strconv.FormatInt(time.Now().Unix(), 10)})
	st.AddHost("192.168.1.10", "Gaming PC", "", "lan", 3_000_000_000, 1000, 0, 0)
	st.AddHost("192.168.1.11", "", "", "lan", 5, 5, 0, 0)
	st.SetHosts([]store.LiveHostRow{{HostTotal: store.HostTotal{IP: "192.168.1.10", Name: "Gaming PC"}, RxBps: 25e6}})
	st.SetIfaces([]store.LiveIfaceRow{{Name: "igb0", Label: "WAN", Kind: "wan", InBps: 50e6, OutBps: 5e6}})

	cfg := &config.Config{Hosts: map[string]string{"192.168.1.10": "Gaming PC"}, MQTT: config.MQTT{
		Enabled: true, Broker: "tcp://" + addr, ClientID: "traffic-monitor-test", TopicPrefix: "traffic-monitor",
		DiscoveryPrefix: "homeassistant", Interval: time.Second, Devices: "named", Interfaces: true, Networks: true,
	}}

	var mu sync.Mutex
	got := map[string][]byte{}
	sub := mqtt.NewClient(mqtt.NewClientOptions().AddBroker("tcp://" + addr).SetClientID("ha"))
	if tok := sub.Connect(); tok.Wait() && tok.Error() != nil {
		t.Fatal(tok.Error())
	}
	sub.Subscribe("#", 0, func(_ mqtt.Client, m mqtt.Message) {
		mu.Lock()
		got[m.Topic()] = m.Payload()
		mu.Unlock()
	}).Wait()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { New(cfg, snapshot.New(nil, st)).Run(ctx); close(done) }()

	want := []string{
		"traffic-monitor/availability",
		"traffic-monitor/state",
		"homeassistant/sensor/traffic_monitor/wan_download/config",
		"homeassistant/binary_sensor/traffic_monitor/collector/config",
		"homeassistant/sensor/traffic_monitor/if_igb0_in/config",
		"traffic-monitor/interface/igb0",
		"homeassistant/sensor/traffic_monitor/net_iot_vlan_download/config",
		"homeassistant/sensor/traffic_monitor/dev_192_168_1_10_today_download/config",
		"traffic-monitor/device/192_168_1_10",
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		missing := ""
		for _, w := range want {
			if got[w] == nil {
				missing = w
				break
			}
		}
		mu.Unlock()
		if missing == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never received %s", missing)
		}
		time.Sleep(100 * time.Millisecond)
	}

	mu.Lock()
	var disc map[string]any
	json.Unmarshal(got["homeassistant/sensor/traffic_monitor/wan_download/config"], &disc)
	if disc["state_topic"] != "traffic-monitor/state" || disc["unit_of_measurement"] != "Mbit/s" || disc["device_class"] != "data_rate" || disc["unique_id"] != "traffic_monitor_wan_download" {
		t.Errorf("bad discovery config: %v", disc)
	}
	var devDisc map[string]any
	json.Unmarshal(got["homeassistant/sensor/traffic_monitor/dev_192_168_1_10_today_download/config"], &devDisc)
	if dev := devDisc["device"].(map[string]any); dev["name"] != "Gaming PC" || dev["via_device"] != "traffic_monitor" {
		t.Errorf("bad device block: %v", dev)
	}
	var state struct {
		WAN    snapshot.WAN `json:"wan"`
		Active int          `json:"active_devices"`
		Top    string       `json:"top_device"`
	}
	json.Unmarshal(got["traffic-monitor/state"], &state)
	if state.WAN.DownloadBps != 50e6 || state.Active != 1 || state.Top != "Gaming PC" {
		t.Errorf("bad state: %+v", state)
	}
	if string(got["traffic-monitor/availability"]) != "online" {
		t.Errorf("availability = %s", got["traffic-monitor/availability"])
	}
	// unnamed devices are not exposed in "named" mode
	if got["homeassistant/sensor/traffic_monitor/dev_192_168_1_11_download/config"] != nil {
		t.Error("unnamed device was exposed")
	}
	mu.Unlock()

	cancel()
	<-done
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if string(got["traffic-monitor/availability"]) != "offline" {
		t.Errorf("availability after shutdown = %s", got["traffic-monitor/availability"])
	}
}
