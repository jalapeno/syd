// Command syd is the SR Network Controller — an SDN control plane for
// SRv6-based path programming in AI fabrics and other use cases.
//
// Usage:
//
//	syd [flags]
//
// Flags:
//
//	--addr              HTTP listen address (default: ":8080")
//
//	--bmp               Enable the BMP/GoBMP NATS collector
//	--nats-url          NATS server URL (default: "nats://localhost:4222")
//	--bmp-consumer      Durable JetStream consumer name prefix (default: "syd")
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
//
//	--compose           Auto-compose recipe: name=src1,src2,... (repeatable)
//	--compose-hold-down Minimum quiescent period after last topology write before
//	                    auto-compose fires (default: 15s). Prevents composing a
//	                    mid-convergence snapshot during a BGP clear-bgp storm.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jalapeno/syd/internal/allocation"
	"github.com/jalapeno/syd/internal/api"
	"github.com/jalapeno/syd/internal/bmpcollector"
	"github.com/jalapeno/syd/internal/graph"
	"github.com/jalapeno/syd/internal/southbound"
	"github.com/jalapeno/syd/internal/southbound/gnmi"
	"github.com/jalapeno/syd/internal/southbound/noop"
	uiembed "github.com/jalapeno/syd/ui"
)

// composeFlag is a repeatable flag value for --compose name=src1,src2,...
type composeFlag []api.ComposeRecipe

func (f *composeFlag) String() string {
	if len(*f) == 0 {
		return ""
	}
	parts := make([]string, len(*f))
	for i, r := range *f {
		parts[i] = r.TargetID + "=" + strings.Join(r.SourceIDs, ",")
	}
	return strings.Join(parts, " ")
}

func (f *composeFlag) Set(s string) error {
	idx := strings.IndexByte(s, '=')
	if idx < 0 {
		return fmt.Errorf("--compose: expected name=src1,src2,... got %q", s)
	}
	targetID := s[:idx]
	if targetID == "" {
		return fmt.Errorf("--compose: target name must not be empty")
	}
	rawSources := s[idx+1:]
	if rawSources == "" {
		return fmt.Errorf("--compose: sources must not be empty")
	}
	sources := strings.Split(rawSources, ",")
	*f = append(*f, api.ComposeRecipe{TargetID: targetID, SourceIDs: sources})
	return nil
}

func main() {
	addr        := flag.String("addr",         ":8080",                  "HTTP listen address")
	bmpEnabled  := flag.Bool("bmp",            false,                    "Enable BMP/GoBMP NATS collector")
	natsURL     := flag.String("nats-url",     "nats://localhost:4222",  "NATS server URL")
	bmpConsumer := flag.String("bmp-consumer", "syd",                        "Durable JetStream consumer name prefix")
	bmpTopo     := flag.String("bmp-topo",     "underlay",               "Topology ID for BMP-learned underlay graph")
	encapMode   := flag.String("encap-mode",   "host",                   "Southbound encap mode: host or tor")
	targetMap   := flag.String("gnmi-target-map", "",                    "nodeID=host:port,... for gNMI target resolution")
	composeHoldDown := flag.Duration("compose-hold-down", 15*time.Second,
		"Minimum quiet period after last topology change before auto-compose fires")
	var composeRecipes composeFlag
	flag.Var(&composeRecipes, "compose", "Auto-compose: name=src1,src2,... (repeatable)")
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
		// DefaultHandlers appends "-v6" to bmpTopo for the primary SRv6 graph.
		primaryTopo := *bmpTopo + "-v6"
		tables.Put(primaryTopo, allocation.NewTable(primaryTopo))

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

	// Start auto-compose loops for any --compose recipes.
	if len(composeRecipes) > 0 {
		srv.StartAutoCompose(ctx, []api.ComposeRecipe(composeRecipes), *composeHoldDown)
		for _, r := range composeRecipes {
			log.Info("auto-compose registered",
				"target", r.TargetID,
				"sources", r.SourceIDs,
			)
		}
	}

	// Combine API routes with embedded UI static assets.
	// API routes take priority; unmatched paths fall through to the UI SPA.
	apiHandler := srv.Handler()
	uiHandler := uiembed.Handler()
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// API paths are handled by the API mux
		switch {
		case len(r.URL.Path) >= 9 && r.URL.Path[:9] == "/topology",
			len(r.URL.Path) >= 6 && r.URL.Path[:6] == "/paths":
			apiHandler.ServeHTTP(w, r)
		default:
			uiHandler.ServeHTTP(w, r)
		}
	})

	log.Info("syd starting",
		"addr", *addr,
		"bmp", *bmpEnabled,
		"encap_mode", *encapMode,
	)
	if err := http.ListenAndServe(*addr, root); err != nil {
		log.Error("server exited", "err", err)
		os.Exit(1)
	}
}
