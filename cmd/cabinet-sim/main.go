// Command cabinet-sim runs one simulated traffic cabinet: a fixed device set
// (asc-1, cam-1, cam-2, lidar-1, dms-1, gw) ticking on a shared clock and
// publishing CloudEvents to the cabinet's local JetStream buffer, with an
// HTTP surface for health checks and scenario injection.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/Vikasa2M/vikasa-demo/internal/sim"
)

const (
	tickPeriod     = 250 * time.Millisecond
	connectRetries = 60
	connectDelay   = 2 * time.Second
	drainTimeout   = 5 * time.Second
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to cabinet config YAML")
	flag.Parse()

	cfg, err := sim.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("cabinet-sim: load config: %v", err)
	}

	nc, err := connectNATS(cfg.NATSURL)
	if err != nil {
		log.Fatalf("cabinet-sim: connect NATS at %s: %v", cfg.NATSURL, err)
	}
	defer nc.Close()

	js, err := nc.JetStream()
	if err != nil {
		log.Fatalf("cabinet-sim: jetstream context: %v", err)
	}
	if err := sim.EnsureBuffer(js, cfg.Dot, cfg.District, cfg.Cabinet); err != nil {
		log.Fatalf("cabinet-sim: ensure buffer: %v", err)
	}
	pub, err := sim.NewPublisher(nc, cfg)
	if err != nil {
		log.Fatalf("cabinet-sim: new publisher: %v", err)
	}

	demand := sim.NewDemand(cfg.Seed, cfg.BaseVPH)
	devices := sim.Devices{
		Demand: demand,
		ASC:    sim.NewASC("asc-1", demand),
		Cam1:   sim.NewCamera("cam-1", demand, 2),
		Cam2:   sim.NewCamera("cam-2", demand, 2),
		Lidar1: sim.NewLidar("lidar-1", demand),
		DMS1:   sim.NewDMS("dms-1", demand),
		GW:     sim.NewGateway("gw"),
	}
	if cfg.Reversible {
		devices.Reversible = sim.NewReversibleLane("rev-1")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	go runTicker(ctx, devices, pub)

	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: newMux(cfg, pub, devices)}
	go func() {
		log.Printf("cabinet-sim: %s listening on %s", cfg.Cabinet, cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("cabinet-sim: http server error: %v", err)
		}
	}()

	<-ctx.Done()
	log.Printf("cabinet-sim: %s draining", cfg.Cabinet)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), drainTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("cabinet-sim: http shutdown: %v", err)
	}
	// Flush in-flight async publishes (Emit doesn't wait for acks) before
	// draining the connection, so buffered events aren't lost on shutdown.
	if err := pub.Flush(drainTimeout); err != nil {
		log.Printf("cabinet-sim: flush pending publishes: %v", err)
	}
	if err := nc.Drain(); err != nil {
		log.Printf("cabinet-sim: nats drain: %v", err)
	}
}

// connectNATS retries the initial connection (60 attempts, 2s apart — ~2min)
// since cabinet-sim can start before its local NATS leaf is ready.
func connectNATS(url string) (*nats.Conn, error) {
	var err error
	for i := 1; i <= connectRetries; i++ {
		var nc *nats.Conn
		if nc, err = nats.Connect(url); err == nil {
			return nc, nil
		}
		log.Printf("cabinet-sim: NATS connect attempt %d/%d to %s failed: %v", i, connectRetries, url, err)
		time.Sleep(connectDelay)
	}
	return nil, err
}

// runTicker drives every device's Tick every 250ms until ctx is done.
func runTicker(ctx context.Context, devices sim.Devices, pub *sim.Publisher) {
	ticker := time.NewTicker(tickPeriod)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			for _, dev := range devices.All() {
				dev.Tick(now, pub)
			}
		case <-ctx.Done():
			return
		}
	}
}

// newMux builds GET /healthz and POST /inject/{scenario}. Handlers run on
// their own goroutines and call device scenario hooks directly — safe
// alongside the ticker goroutine since each device (and Demand) guards its
// own state with a mutex.
func newMux(cfg sim.Config, pub *sim.Publisher, devices sim.Devices) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  "ok",
			"cabinet": cfg.Cabinet,
			"dropped": pub.Dropped(),
		})
	})

	mux.HandleFunc("POST /inject/{scenario}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("scenario")
		if err := sim.Scenario(name, devices); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})

	return mux
}
