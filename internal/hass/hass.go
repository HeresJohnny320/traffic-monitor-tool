// Package hass publishes Traffic Monitor data to MQTT using Home Assistant MQTT
// discovery: with the MQTT integration set up in Home Assistant, Traffic Monitor,
// its firewall interfaces, networks and chosen client devices appear as
// devices with sensors automatically - no YAML on the Home Assistant side.
package hass

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"log"
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"trafficmonitor/internal/config"
	"trafficmonitor/internal/snapshot"
)

// maxHADevices caps client devices created in Home Assistant (relevant with
// devices: all on large or IPv6-heavy networks).
const maxHADevices = 500

type Publisher struct {
	cfg    config.MQTT
	hosts  map[string]bool // IPs named in the config's `hosts:`
	src    *snapshot.Source
	inst   string // id prefix, lets several Traffic Monitor instances share a broker
	client mqtt.Client

	mu        sync.Mutex
	announced map[string]bool            // discovery configs already sent
	devices   map[string]snapshot.Device // client devices with entities
	publish   func(topic string, retain bool, payload []byte)
}

func New(cfg *config.Config, src *snapshot.Source) *Publisher {
	p := &Publisher{
		cfg:       cfg.MQTT,
		hosts:     map[string]bool{},
		src:       src,
		inst:      slug(cfg.MQTT.TopicPrefix),
		announced: map[string]bool{},
		devices:   map[string]snapshot.Device{},
	}
	for ip := range cfg.Hosts {
		if a, err := netip.ParseAddr(ip); err == nil {
			p.hosts[a.String()] = true
		}
	}
	return p
}

func (p *Publisher) availTopic() string { return p.cfg.TopicPrefix + "/availability" }

func (p *Publisher) Run(ctx context.Context) {
	opts := mqtt.NewClientOptions().
		AddBroker(p.cfg.Broker).
		SetClientID(p.cfg.ClientID).
		SetUsername(p.cfg.Username).
		SetPassword(p.cfg.Password).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(10*time.Second).
		SetWill(p.availTopic(), "offline", 1, true).
		SetConnectionLostHandler(func(_ mqtt.Client, err error) { log.Printf("mqtt: connection lost: %v", err) }).
		SetOnConnectHandler(func(c mqtt.Client) {
			log.Printf("mqtt: connected to %s", p.cfg.Broker)
			c.Publish(p.availTopic(), 1, true, "online")
			p.forgetAnnounced() // (re)send discovery after every connect
			// Home Assistant announces restarts here; resend discovery then too
			c.Subscribe(p.cfg.DiscoveryPrefix+"/status", 0, func(_ mqtt.Client, m mqtt.Message) {
				if string(m.Payload()) == "online" {
					p.forgetAnnounced()
				}
			})
			go p.publishAll(ctx)
		})
	if p.cfg.TLSInsecure {
		opts.SetTLSConfig(&tls.Config{InsecureSkipVerify: true})
	}
	p.client = mqtt.NewClient(opts)
	p.publish = func(topic string, retain bool, payload []byte) {
		qos := byte(0)
		if retain {
			qos = 1
		}
		p.client.Publish(topic, qos, retain, payload)
	}
	log.Printf("mqtt: connecting to %s (Home Assistant discovery prefix %q)", p.cfg.Broker, p.cfg.DiscoveryPrefix)
	p.client.Connect() // retries in the background until it succeeds

	t := time.NewTicker(p.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			if p.client.IsConnected() {
				p.client.Publish(p.availTopic(), 1, true, "offline").WaitTimeout(2 * time.Second)
			}
			p.client.Disconnect(500)
			return
		case <-t.C:
			if p.client.IsConnected() {
				p.publishAll(ctx)
			}
		}
	}
}

func (p *Publisher) forgetAnnounced() {
	p.mu.Lock()
	p.announced = map[string]bool{}
	p.mu.Unlock()
}

// ---------------------------------------------------------------- entities

type haDevice struct {
	Identifiers  []string `json:"identifiers"`
	Name         string   `json:"name"`
	Manufacturer string   `json:"manufacturer,omitempty"`
	Model        string   `json:"model,omitempty"`
	ViaDevice    string   `json:"via_device,omitempty"`
}

type entity struct {
	component  string // sensor | binary_sensor
	id         string // object id, unique within this instance
	name       string
	template   string
	unit       string
	class      string // device_class
	stateClass string
	icon       string
	precision  int // suggested_display_precision, -1 = unset
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	return strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "_"), "_")
}

const (
	mbps = "{{ (value_json.%s / 1000000) | round(3) }}"
	gb   = "{{ (value_json.%s / 1000000000) | round(3) }}"
)

func rate(id, name, field, icon string) entity {
	return entity{"sensor", id, name, strings.Replace(mbps, "%s", field, 1), "Mbit/s", "data_rate", "measurement", icon, 2}
}

func total(id, name, field, icon string) entity {
	return entity{"sensor", id, name, strings.Replace(gb, "%s", field, 1), "GB", "data_size", "total_increasing", icon, 2}
}

func (p *Publisher) hubDevice() haDevice {
	return haDevice{Identifiers: []string{p.inst}, Name: "Traffic Monitor", Manufacturer: "Traffic Monitor", Model: "pfSense / OPNsense traffic monitor"}
}

// announce sends discovery configs for a set of entities sharing a state topic,
// once per connection.
func (p *Publisher) announce(dev haDevice, stateTopic string, ents []entity) {
	for _, e := range ents {
		key := e.component + "/" + e.id + "|" + dev.Name // a rename resends discovery
		p.mu.Lock()
		done := p.announced[key]
		p.announced[key] = true
		p.mu.Unlock()
		if done {
			continue
		}
		cfg := map[string]any{
			"name":               e.name,
			"unique_id":          p.inst + "_" + e.id,
			"object_id":          p.inst + "_" + e.id,
			"state_topic":        stateTopic,
			"value_template":     e.template,
			"availability_topic": p.availTopic(),
			"device":             dev,
		}
		for k, v := range map[string]string{"unit_of_measurement": e.unit, "device_class": e.class, "state_class": e.stateClass, "icon": e.icon} {
			if v != "" {
				cfg[k] = v
			}
		}
		if e.precision >= 0 && e.component == "sensor" && e.unit != "" {
			cfg["suggested_display_precision"] = e.precision
		}
		b, _ := json.Marshal(cfg)
		p.publish(p.cfg.DiscoveryPrefix+"/"+e.component+"/"+p.inst+"/"+e.id+"/config", true, b)
	}
}

func (p *Publisher) state(topic string, v any) {
	b, err := json.Marshal(v)
	if err == nil {
		p.publish(topic, false, b)
	}
}

// wantDevice decides whether a client device gets its own HA device.
func (p *Publisher) wantDevice(d snapshot.Device, custom bool) bool {
	for _, x := range p.cfg.DeviceList {
		if x == d.IP || (d.Name != "" && strings.EqualFold(x, d.Name)) {
			return true
		}
	}
	switch p.cfg.Devices {
	case "all":
		return true
	case "named":
		return p.hosts[d.IP] || custom
	}
	return false
}

func (p *Publisher) publishAll(ctx context.Context) {
	snap, err := p.src.Take(ctx)
	if err != nil {
		log.Printf("mqtt: snapshot: %v", err)
		return
	}
	hub := p.hubDevice()
	hubID := hub.Identifiers[0]
	pre := p.cfg.TopicPrefix

	// --- Traffic Monitor itself: WAN and overall numbers
	topName, topDl := "none", 0.0
	if len(snap.Devices) > 0 && snap.Devices[0].Active {
		topName, topDl = snap.Devices[0].Name, snap.Devices[0].DownloadBps
		if topName == "" {
			topName = snap.Devices[0].IP
		}
	}
	p.announce(hub, pre+"/state", []entity{
		rate("wan_download", "WAN download", "wan.download_bps", "mdi:download"),
		rate("wan_upload", "WAN upload", "wan.upload_bps", "mdi:upload"),
		total("wan_today_download", "WAN downloaded today", "wan.today_download_bytes", "mdi:download-box"),
		total("wan_today_upload", "WAN uploaded today", "wan.today_upload_bytes", "mdi:upload-box"),
		{"sensor", "active_devices", "Active devices", "{{ value_json.active_devices }}", "", "", "measurement", "mdi:devices", -1},
		{"sensor", "known_devices", "Known devices", "{{ value_json.known_devices }}", "", "", "measurement", "mdi:lan", -1},
		{"sensor", "top_device", "Top device", "{{ value_json.top_device }}", "", "", "", "mdi:trophy", -1},
		{"binary_sensor", "collector", "Collector", "{{ 'ON' if value_json.collector_alive else 'OFF' }}", "", "connectivity", "", "", -1},
	})
	p.state(pre+"/state", map[string]any{
		"collector_alive":         snapshot.CollectorAlive(snap.Meta),
		"mode":                    p.src.Mode(),
		"wan":                     snap.WAN,
		"active_devices":          snap.ActiveDevices(),
		"known_devices":           len(snap.Devices),
		"top_device":              topName,
		"top_device_download_bps": topDl,
	})

	// --- firewall interfaces (WAN, LAN, VLANs, VPN tunnels)
	if p.cfg.Interfaces {
		for _, f := range snap.Ifaces {
			s := slug(f.Name)
			topic := pre + "/interface/" + s
			dev := haDevice{Identifiers: []string{hubID + "_if_" + s}, Name: "Traffic Monitor " + f.Label, Manufacturer: "Traffic Monitor", Model: strings.ToUpper(f.Kind) + " interface " + f.Name, ViaDevice: hubID}
			p.announce(dev, topic, []entity{
				rate("if_"+s+"_in", "In", "in_bps", "mdi:arrow-down-bold"),
				rate("if_"+s+"_out", "Out", "out_bps", "mdi:arrow-up-bold"),
				total("if_"+s+"_today_in", "In today", "today_in_bytes", "mdi:arrow-down-bold-box"),
				total("if_"+s+"_today_out", "Out today", "today_out_bytes", "mdi:arrow-up-bold-box"),
			})
			p.state(topic, f)
		}
	}

	// --- networks (LAN, each VLAN, each VPN subnet)
	if p.cfg.Networks {
		for _, n := range snap.Networks {
			s := slug(n.Name)
			topic := pre + "/network/" + s
			dev := haDevice{Identifiers: []string{hubID + "_net_" + s}, Name: n.Name + " network", Manufacturer: "Traffic Monitor", Model: strings.ToUpper(n.Kind) + " " + n.CIDRs, ViaDevice: hubID}
			p.announce(dev, topic, []entity{
				rate("net_"+s+"_download", "Download", "download_bps", "mdi:download"),
				rate("net_"+s+"_upload", "Upload", "upload_bps", "mdi:upload"),
				total("net_"+s+"_today_download", "Downloaded today", "today_download_bytes", "mdi:download-box"),
				total("net_"+s+"_today_upload", "Uploaded today", "today_upload_bytes", "mdi:upload-box"),
				{"sensor", "net_" + s + "_active", "Active devices", "{{ value_json.active_devices }}", "", "", "measurement", "mdi:devices", -1},
			})
			p.state(topic, n)
		}
	}

	// --- client devices
	names := p.src.HostNames(ctx)
	seen := map[string]bool{}
	for _, d := range snap.Devices {
		if !p.wantDevice(d, names[d.IP].CustomName != "") {
			continue
		}
		p.mu.Lock()
		_, known := p.devices[d.IP]
		full := len(p.devices) >= maxHADevices
		p.mu.Unlock()
		if !known && full {
			continue // don't flood Home Assistant with entities
		}
		seen[d.IP] = true
		p.mu.Lock()
		p.devices[d.IP] = d
		p.mu.Unlock()
		p.publishDevice(hubID, d)
	}
	// devices that dropped out of the snapshot (idle, or day rollover) read zero
	p.mu.Lock()
	var stale []snapshot.Device
	for ip, d := range p.devices {
		if !seen[ip] {
			stale = append(stale, snapshot.Device{IP: ip, Name: d.Name, Network: d.Network, Kind: d.Kind})
		}
	}
	p.mu.Unlock()
	for _, d := range stale {
		p.publishDevice(hubID, d)
	}
}

func (p *Publisher) publishDevice(hubID string, d snapshot.Device) {
	s := slug(d.IP)
	topic := p.cfg.TopicPrefix + "/device/" + s
	name := d.Name
	if name == "" {
		name = d.IP
	}
	dev := haDevice{Identifiers: []string{hubID + "_dev_" + s}, Name: name, Manufacturer: "Traffic Monitor", Model: strings.TrimSpace(d.Network + " client " + d.IP), ViaDevice: hubID}
	p.announce(dev, topic, []entity{
		rate("dev_"+s+"_download", "Download", "download_bps", "mdi:download"),
		rate("dev_"+s+"_upload", "Upload", "upload_bps", "mdi:upload"),
		total("dev_"+s+"_today_download", "Downloaded today", "today_download_bytes", "mdi:download-box"),
		total("dev_"+s+"_today_upload", "Uploaded today", "today_upload_bytes", "mdi:upload-box"),
		{"binary_sensor", "dev_" + s + "_active", "Active", "{{ 'ON' if value_json.active else 'OFF' }}", "", "", "", "mdi:lan-connect", -1},
	})
	p.state(topic, d)
}
