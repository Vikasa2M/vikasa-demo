// internal/sim/gateway.go
package sim

import (
	"sync"
	"time"

	openitspb "github.com/openits/openits-models/pkg/proto/openits/v1"
)

const (
	ceTypeGatewayHeartbeat = "vikasa.gateway.heartbeat.v1"
	gatewayHeartbeatPeriod = 15 * time.Second
)

// Gateway simulates the cabinet's NATS poller/gateway: it has no domain
// traffic of its own, just a periodic PollerHeartbeat reporting the cabinet's
// health to the fleet backend. Same Tick-driven, no-goroutine design as ASC.
type Gateway struct {
	mu         sync.Mutex
	controller string

	startedAt     time.Time
	nextHeartbeat time.Time
}

// NewGateway creates a Gateway device for controller.
func NewGateway(controller string) *Gateway {
	return &Gateway{controller: controller}
}

// Tick advances the Gateway to now, emitting any heartbeats that came due.
func (g *Gateway) Tick(now time.Time, em Emitter) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.startedAt.IsZero() {
		g.startedAt = now
	}
	if g.nextHeartbeat.IsZero() {
		g.nextHeartbeat = now.Add(gatewayHeartbeatPeriod)
	}
	for !now.Before(g.nextHeartbeat) {
		at := g.nextHeartbeat
		msg := &openitspb.PollerHeartbeat{
			PollerId:        g.controller,
			TimestampUnixMs: at.UnixMilli(),
			UptimeSeconds:   int64(at.Sub(g.startedAt).Seconds()),
			DevicesTotal:    1,
			DevicesHealthy:  1,
			NatsConnected:   true,
		}
		em.Emit(g.controller, ceTypeGatewayHeartbeat, at, msg)
		g.nextHeartbeat = at.Add(gatewayHeartbeatPeriod)
	}
}
