// Package bmpcollector subscribes to GoBMP's NATS JetStream output and
// translates BGP monitoring messages into graph updates.
//
// Architecture:
//
//	GoBMP ──NATS JetStream──► Collector ──dispatch──► MessageHandler
//	                                                         │
//	                                                         ▼
//	                                                   graph.Store
//
// The MessageHandler interface is the extension point for new AFI/SAFIs.
// The four initial handlers cover BGP-LS (LSNode, LSLink, LSSRv6SID, Peer).
// Future handlers (unicast prefix, L3VPN, EVPN, SR policy) can be added by
// implementing the interface and registering them with Collector.Register.
package bmpcollector

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/jalapeno/scoville/internal/graph"
)

// GoBMP NATS subject constants. These mirror the hardcoded constants in
// GoBMP's nats-publisher.go so that both sides stay in sync.
const (
	SubjectPeer      = "gobmp.parsed.peer"
	SubjectLSNode    = "gobmp.parsed.ls_node"
	SubjectLSLink    = "gobmp.parsed.ls_link"
	SubjectLSSRv6SID = "gobmp.parsed.ls_srv6_sid"
	SubjectLSPrefix  = "gobmp.parsed.ls_prefix"

	// Future AFI/SAFI subjects — not handled yet but listed here so callers
	// can reference them by name when registering custom handlers.
	SubjectUnicastV4  = "gobmp.parsed.unicast_prefix_v4"
	SubjectUnicastV6  = "gobmp.parsed.unicast_prefix_v6"
	SubjectL3VPNV4    = "gobmp.parsed.l3vpn_v4"
	SubjectL3VPNV6    = "gobmp.parsed.l3vpn_v6"
	SubjectEVPN       = "gobmp.parsed.evpn"
	SubjectSRPolicyV4 = "gobmp.parsed.sr_policy_v4"
	SubjectSRPolicyV6 = "gobmp.parsed.sr_policy_v6"
	SubjectFlowspecV4 = "gobmp.parsed.flowspec_v4"
	SubjectFlowspecV6 = "gobmp.parsed.flowspec_v6"
	SubjectStatistics = "gobmp.parsed.statistics"

	// GoBMPStream is the JetStream stream name GoBMP creates.
	GoBMPStream = "goBMP"
)

// MessageHandler processes messages from a single NATS subject and applies
// the result to the graph store. Implement this interface to add support for
// new AFI/SAFIs without changing the collector core.
type MessageHandler interface {
	// Subject returns the NATS subject this handler processes,
	// e.g. "gobmp.parsed.ls_node".
	Subject() string

	// Handle deserializes data and applies graph updates to store.
	// It must be safe to call concurrently from multiple goroutines.
	Handle(data []byte, store *graph.Store) error
}

// Config controls how the Collector connects to NATS.
type Config struct {
	// NATSUrl is the NATS server URL, e.g. "nats://localhost:4222".
	NATSUrl string

	// ConsumerName is the durable JetStream consumer prefix. Scoped per
	// subject, so each subject gets an independent consumer position.
	// Must be unique per scoville instance if multiple instances share the same
	// NATS server.
	ConsumerName string
}

// Collector subscribes to GoBMP's NATS output, dispatches each message to the
// registered MessageHandler for that subject, and applies resulting graph
// updates. New AFI/SAFIs are supported by calling Register before Start.
type Collector struct {
	cfg      Config
	store    *graph.Store
	handlers map[string]MessageHandler
	log      *slog.Logger

	nc   *nats.Conn
	js   nats.JetStreamContext
	subs []*nats.Subscription
}

// New creates a Collector with no handlers registered. Call Register to
// install handlers before calling Start.
func New(cfg Config, store *graph.Store, log *slog.Logger) *Collector {
	return &Collector{
		cfg:      cfg,
		store:    store,
		handlers: make(map[string]MessageHandler),
		log:      log,
	}
}

// Register installs h for the subject it declares. Calling Register with the
// same subject twice replaces the previous handler.
func (c *Collector) Register(h MessageHandler) {
	c.handlers[h.Subject()] = h
}

// Start connects to NATS, creates a durable JetStream consumer for each
// registered handler, and dispatches incoming messages. It blocks until ctx
// is cancelled. If GoBMP's JetStream stream does not exist yet (startup race),
// Start retries with backoff until the stream appears or ctx is cancelled.
func (c *Collector) Start(ctx context.Context) error {
	nc, err := nats.Connect(c.cfg.NATSUrl,
		nats.Name("scoville-bmpcollector"),
		nats.MaxReconnects(-1), // reconnect indefinitely
		nats.ReconnectWait(2*time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			c.log.Warn("nats disconnected", "err", err)
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			c.log.Info("nats reconnected", "url", nc.ConnectedUrl())
		}),
	)
	if err != nil {
		return fmt.Errorf("nats connect %s: %w", c.cfg.NATSUrl, err)
	}
	c.nc = nc

	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return fmt.Errorf("jetstream context: %w", err)
	}
	c.js = js

	// GoBMP creates its stream lazily on first connect. Retry subscribing
	// with backoff until the stream exists or ctx is cancelled.
	retryDelay := 3 * time.Second
	for {
		err := c.subscribeAll()
		if err == nil {
			break
		}
		c.log.Warn("bmp subscribe failed, retrying", "err", err, "delay", retryDelay)
		select {
		case <-ctx.Done():
			c.Stop()
			return nil
		case <-time.After(retryDelay):
			if retryDelay < 30*time.Second {
				retryDelay *= 2
			}
		}
	}

	c.log.Info("bmp collector started",
		"nats_url", c.cfg.NATSUrl,
		"handlers", len(c.handlers),
	)

	<-ctx.Done()
	return nil
}

// subscribeAll attempts to create JetStream consumers for all registered
// handlers. Returns the first error encountered.
func (c *Collector) subscribeAll() error {
	// Drain any previous subscriptions from a prior attempt.
	for _, sub := range c.subs {
		_ = sub.Drain()
	}
	c.subs = nil

	for _, h := range c.handlers {
		sub, err := c.subscribe(h)
		if err != nil {
			return fmt.Errorf("subscribe %s: %w", h.Subject(), err)
		}
		c.subs = append(c.subs, sub)
	}
	return nil
}

// Stop drains all subscriptions and closes the NATS connection gracefully.
func (c *Collector) Stop() {
	for _, sub := range c.subs {
		_ = sub.Drain()
	}
	if c.nc != nil {
		_ = c.nc.Drain()
	}
}

// subscribe creates a durable JetStream push consumer for h. The consumer
// uses DeliverLastPerSubjectPolicy so the collector receives the current
// state for each key immediately on connect or reconnect, without waiting for
// routers to re-advertise.
func (c *Collector) subscribe(h MessageHandler) (*nats.Subscription, error) {
	// Durable names must be unique per subject and must not contain dots.
	durableName := c.cfg.ConsumerName + "-" + sanitizeDots(h.Subject())

	sub, err := c.js.Subscribe(
		h.Subject(),
		func(msg *nats.Msg) {
			if err := h.Handle(msg.Data, c.store); err != nil {
				c.log.Warn("handler error",
					"subject", h.Subject(),
					"err", err,
					"bytes", len(msg.Data),
				)
			}
			_ = msg.Ack()
		},
		nats.Durable(durableName),
		nats.DeliverNew(),   // InterestPolicy stream: no stored msgs to replay
		nats.AckExplicit(),
		// No BindStream: let NATS auto-find the stream by subject match.
		// GoBMP creates "goBMP" stream covering gobmp.parsed.* on startup.
	)
	if err != nil {
		return nil, err
	}
	return sub, nil
}

// sanitizeDots replaces dots with hyphens to produce a valid NATS consumer
// name (consumer names must not contain dots).
func sanitizeDots(s string) string {
	out := []byte(s)
	for i, b := range out {
		if b == '.' {
			out[i] = '-'
		}
	}
	return string(out)
}
