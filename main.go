// traffic-monitor: per-IP and per-interface traffic monitor for pfSense / OPNsense.
//
//	traffic-monitor collector -config traffic-monitor.yaml   # receive NetFlow + poll SNMP, write to DB
//	traffic-monitor web       -config traffic-monitor.yaml   # dashboard that reads the DB
//	traffic-monitor all       -config traffic-monitor.yaml   # both in one process
//
// Outputs can be switched on and off independently: database (history),
// web.ui (dashboard), api (/api/v1 + /metrics) and mqtt (Home Assistant
// discovery). With database.enabled: false nothing is stored; the dashboard,
// API and MQTT then serve live data from the collector's memory.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"trafficmonitor/internal/collector"
	"trafficmonitor/internal/config"
	"trafficmonitor/internal/hass"
	"trafficmonitor/internal/live"
	"trafficmonitor/internal/snapshot"
	"trafficmonitor/internal/store"
	"trafficmonitor/internal/web"
)

// version is set at build time: -ldflags "-X main.version=v1.2.3"
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd := os.Args[1]
	if cmd == "version" || cmd == "-version" || cmd == "--version" {
		fmt.Println("traffic-monitor", version)
		return
	}
	fsFlags := flag.NewFlagSet(cmd, flag.ExitOnError)
	cfgPath := fsFlags.String("config", "traffic-monitor.yaml", "path to config file (created by the Settings page if missing)")
	fsFlags.Parse(os.Args[2:])

	if cmd != "collector" && cmd != "web" && cmd != "all" {
		usage()
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	if _, err := os.Stat(*cfgPath); err != nil {
		log.Printf("config: %s not found, starting with defaults (configure in the dashboard's Settings tab)", *cfgPath)
		if cmd != "collector" {
			freeWebPort(cfg, *cfgPath)
		}
	}
	if err := cfg.CheckOutputs(cmd); err != nil {
		log.Fatal(err)
	}
	log.Printf("traffic-monitor %s starting (%s)", version, cmd)

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// live state survives settings reloads, so live rates and today's
	// counters don't reset; a standalone `web` reads them from the DB instead
	var state *live.State
	if cmd != "web" {
		state = live.New()
	}
	for {
		var reloading atomic.Bool
		ctx, cancel := context.WithCancel(sigCtx)
		reload := func() { reloading.Store(true); cancel() }
		err := run(ctx, cmd, *cfgPath, cfg, state, reload)
		cancel()
		if err != nil {
			log.Fatal(err)
		}
		if sigCtx.Err() != nil || !reloading.Load() {
			return
		}
		next, err := config.Load(*cfgPath)
		if err != nil {
			log.Printf("settings: %v (keeping the previous settings)", err)
		} else {
			cfg = next
		}
		log.Printf("settings: applied, restarting components")
	}
}

// run starts every enabled component for cfg and blocks until ctx is
// canceled (shutdown or settings reload) or a component fails.
func run(parent context.Context, cmd, cfgPath string, cfg *config.Config, state *live.State, reload func()) error {
	ctx, fail := context.WithCancelCause(parent)
	defer fail(nil)

	runsCollector := cmd == "collector" || cmd == "all"
	runsWeb := (cmd == "web" || cmd == "all") && (cfg.Web.UI || cfg.API.Enabled)
	runsMQTT := runsCollector && cfg.MQTT.Enabled

	var db *store.DB
	if cfg.Database.Enabled {
		var err error
		if db, err = store.Open(cfg.Database.Driver, cfg.Database.DSN); err != nil {
			return err
		}
		defer db.Close()
	}
	if runsWeb && cfg.API.Enabled && len(cfg.API.Tokens) == 0 {
		log.Printf("api: /api/v1 and /metrics are open without a token (set one in Settings)")
	}
	src := snapshot.New(db, state)

	var wg sync.WaitGroup
	if runsCollector {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := collector.New(cfg, db, state).Run(ctx); err != nil {
				fail(fmt.Errorf("collector: %w", err))
			}
		}()
		if cfg.SNMP.Enabled {
			wg.Add(1)
			go func() { defer wg.Done(); collector.NewSNMPPoller(cfg, db, state).Run(ctx) }()
		}
	}
	if runsMQTT {
		wg.Add(1)
		go func() { defer wg.Done(); hass.New(cfg, src).Run(ctx) }()
	}
	if runsWeb {
		opts := web.Options{
			UI: cfg.Web.UI, User: cfg.Web.Username, Pass: cfg.Web.Password, API: cfg.API,
			Config: cfg, ConfigPath: cfgPath, Cmd: cmd, Reload: reload, Version: version,
			GUIToken: guiToken(cfgPath),
		}
		srv := &http.Server{
			Addr: cfg.Web.Listen, Handler: web.New(db, src, opts).Handler(),
			ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
			WriteTimeout: 60 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 64 << 10,
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			log.Printf("web: http://%s", cfg.Web.Listen)
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				fail(fmt.Errorf("web: %w", err))
			}
		}()
		go func() {
			<-ctx.Done()
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			srv.Shutdown(sctx)
		}()
	}
	<-ctx.Done()
	wg.Wait()
	if err := context.Cause(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// freeWebPort is for a first run without a config file: if the dashboard's
// port (8080) is taken, it uses the next free one (8081, 8082, ...) and saves
// it to the config file so it stays the same. Change it under Settings.
func freeWebPort(cfg *config.Config, cfgPath string) {
	host, portStr, err := net.SplitHostPort(cfg.Web.Listen)
	port, perr := strconv.Atoi(portStr)
	if err != nil || perr != nil {
		return
	}
	for p := port; p < port+20; p++ {
		addr := net.JoinHostPort(host, strconv.Itoa(p))
		l, err := net.Listen("tcp", addr)
		if err != nil {
			continue
		}
		l.Close()
		if p == port {
			return
		}
		cfg.Web.Listen = addr
		log.Printf("web: port %d is in use, using %d instead", port, p)
		if err := config.Save(cfgPath, cfg); err != nil {
			log.Printf("config: saving %s: %v (the port may change on the next start)", cfgPath, err)
		}
		return
	}
}

// guiToken reads the secret shared with the firewall's proxy page (installed
// by install.sh on pfSense/OPNsense) from gui-token next to the config file.
// No file = the dashboard isn't offered inside the firewall's web UI.
func guiToken(cfgPath string) string {
	b, err := os.ReadFile(filepath.Join(filepath.Dir(cfgPath), "gui-token"))
	if err != nil {
		return ""
	}
	t := strings.TrimSpace(string(b))
	if len(t) < 16 {
		log.Printf("web: ignoring gui-token: too short")
		return ""
	}
	return t
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: traffic-monitor <all|collector|web|version> [-config traffic-monitor.yaml]")
	os.Exit(2)
}
