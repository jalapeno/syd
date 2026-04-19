// Command scoville is the SR Network Controller — an SDN control plane for
// SRv6-based path programming in AI fabrics and other use cases.
//
// Usage:
//
//	scoville [flags]
//
// Flags:
//
//	--addr              HTTP listen address (default: ":8080")
//
//	--bmp               Enable the BMP/GoBMP NATS collector
//	--nats-url          NATS server URL (default: "nats://localhost:4222")
//	--bmp-consumer      Durable JetStream consumer name prefix (default: "scoville")
//	--bmp-topo          Topology ID for BMP-learned underlay graph (default: "underlay")
//
//	--encap-mode        Southbound encap mode: "host" (default) or "tor"
//	                    host — no-op driver; callers fetch segment lists via /flows
//	                    tor  — gNMI push to SONiC ToR switches on workload allocation
//
//	--gnmi-target-map   Comma-separated nodeID=host:port entries for gNMI target
//	                    resolution in BMP-sourced topologies where nodes lack a
//	                    management_ip annotation. Example:
//	                    "spine-1=192.168.0.1:57400,leaf-1=192.168.0.2:57400"
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/jalapeno/scoville/internal/allocation"
	"github.com/jalapeno/scoville/internal/api"
	"github.com/jalapeno/scoville/internal/bmpcollector"
	"github.com/jalapeno/scoville/internal/graph"
	"github.com/jalapeno/scoville/internal/southbound"
	"github.com/jalapeno/scoville/internal/southbound/gnmi"
	"github.com/jalapeno/scoville/internal/southbound/noop"
)

func main() {
	addr        := flag.String("addr",         ":8080",                  "HTTP listen address")
	bmpEnabled  := flag.Bool("bmp",            false,                    "Enable BMP/GoBMP NATS collector")
	natsURL     := flag.String("nats-url",     "nats://localhost:4222",  "NATS server URL")
	bmpConsumer := flag.String("bmp-consumer", "scoville",                   "Durable JetStream consumer name prefix")
	bmpTopo     := flag.String("bmp-topo",     "underlay",               "Topology ID for BMP-learned underlay graph")
	encapMode   := flag.String("encap-mode",   "host",                   "Southbound encap mode: host or tor")
	targetMap   := flag.String("gnmi-target-map", "",                    "nodeID=host:port,... for gNMI target resolution")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	store  := graph.NewStore()
	tables := allocation.NewTableSet()

	// Context tied to OS signals so BMP collector and server shut down cleanly.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// --- Southbound driver ---------------------------------------------------
	var driver southbound.SouthboundDriver
	switch southbound.EncapMode(*encapMode) {
	case southbound.EncapModeToR:
		tm, err := gnmi.ParseTargetMap(*targetMap)
		if err != nil {
			log.Error("invalid --gnmi-target-map", "err", err)
			os.Exit(1)
		}
		// DialFunc is left as a stub until the github.com/openconfig/gnmi
		// dependency is added. Replace this with a real gRPC dialer when
		// wiring up SONiC switches.
		dialFn := func(ctx context.Context, target string) (gnmi.GNMIClient, error) {
			log.Warn("gNMI dial not yet implemented — add openconfig/gnmi dependency",
				"target", target)
			return nil, nil
		}
		driver = gnmi.New(store, tm, dialFn, log)
		log.Info("southbound: gNMI ToR mode", "target_map_entries", len(tm))
	default:
		driver = noop.New()
		log.Info("southbound: host mode (pull via /flows)")
	}

	// --- BMP collector -------------------------------------------------------
	if *bmpEnabled {
		// Pre-create the allocation table for the BMP-learned topology so
		// path requests work as soon as the graph is populated. POST /topology
		// normally does this, but BMP-driven graphs bypass that handler.
		tables.Put(*bmpTopo, allocation.NewTable(*bmpTopo))

		updater := bmpcollector.NewUpdater()

		// Wire topology invalidation: when BMP withdraws a node or link,
		// drain any active workload allocations whose paths traverse it.
		updater.SetRemovalCallback(tables.InvalidateElement)

		cfg := bmpcollector.Config{
			NATSUrl:      *natsURL,
			ConsumerName: *bmpConsumer,
		}
		collector := bmpcollector.New(cfg, store, log)
		for _, h := range bmpcollector.DefaultHandlers(updater, *bmpTopo) {
			collector.Register(h)
		}
		go func() {
			if err := collector.Start(ctx); err != nil {
				log.Error("bmp collector error", "err", err)
			}
		}()
		log.Info("bmp collector configured",
			"nats_url", *natsURL,
			"topo_id", *bmpTopo,
		)
	}

	// --- HTTP API server -----------------------------------------------------
	srv := api.NewWithDriver(store, tables, driver, log)

	log.Info("scoville starting",
		"addr", *addr,
		"bmp", *bmpEnabled,
		"encap_mode", *encapMode,
	)
	if err := http.ListenAndServe(*addr, srv.Handler()); err != nil {
		log.Error("server exited", "err", err)
		os.Exit(1)
	}
}
