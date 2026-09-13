package routing

import (
	"bytes"
	"log/slog"
	"sync"
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
)

// priority levels
const (
	PriorityUrgent = "Urgent"
	PriorityHigh   = "High"
	PriorityNormal = "Normal"
)

// PriorityMessagingConfig configures the aging thresholds for priority queues.
type PriorityMessagingConfig struct {
	AgingThresholdHigh   time.Duration
	AgingThresholdNormal time.Duration
	SchedulerInterval    time.Duration
}

type prioritizedPacket struct {
	cl       *mqtt.Client
	pk       packets.Packet
	enqueued time.Time
}

// PriorityMessagingHook is an MQTT hook that delays packet delivery and schedules them
// via strict priority queues with an aging mechanism.
type PriorityMessagingHook struct {
	mqtt.HookBase
	config PriorityMessagingConfig
	server *mqtt.Server

	urgent []prioritizedPacket
	high   []prioritizedPacket
	normal []prioritizedPacket
	mu     sync.Mutex
	cond   *sync.Cond

	stop     chan struct{}
	initOnce sync.Once

	// startMu guards the readiness flags below and ensures the background
	// goroutines are started at most once, and only after both the config
	// (written by Init) and the server reference (written by SetOpts) are
	// fully assigned, regardless of which of Init/SetOpts mochi-mqtt calls
	// first.
	startMu     sync.Mutex
	configReady bool
	serverReady bool
	started     bool
}

// ID returns the unique identifier for this hook.
func (h *PriorityMessagingHook) ID() string {
	return "priority-messaging"
}

// Provides indicates which hook methods this hook implements.
func (h *PriorityMessagingHook) Provides(b byte) bool {
	return bytes.Contains([]byte{
		mqtt.OnPublish,
	}, []byte{b})
}

// Init initializes the hook with the provided configuration.
func (h *PriorityMessagingHook) Init(config any) error {
	if config == nil {
		h.config = PriorityMessagingConfig{
			AgingThresholdHigh:   500 * time.Millisecond,
			AgingThresholdNormal: 1000 * time.Millisecond,
			SchedulerInterval:    10 * time.Millisecond,
		}
	} else {
		cfg, ok := config.(*PriorityMessagingConfig)
		if !ok {
			return mqtt.ErrInvalidConfigType
		}
		h.config = *cfg
	}

	h.ensureQueues()

	h.startMu.Lock()
	h.configReady = true
	h.startMu.Unlock()
	h.maybeStart()

	return nil
}

// ensureQueues lazily allocates the queues, mutex condition variable, and stop
// channel exactly once. Mochi-MQTT's AddHook calls SetOpts before it calls
// Init, so both entry points funnel through this method to stay correct
// regardless of call order.
func (h *PriorityMessagingHook) ensureQueues() {
	h.initOnce.Do(func() {
		h.urgent = make([]prioritizedPacket, 0)
		h.high = make([]prioritizedPacket, 0)
		h.normal = make([]prioritizedPacket, 0)
		h.cond = sync.NewCond(&h.mu)
		h.stop = make(chan struct{})
	})
}

// maybeStart launches the background scheduler/aging goroutines exactly once,
// and only once both the config (from Init) and the server reference (from
// SetOpts) have been fully assigned. This avoids a data race where the
// goroutines could otherwise start reading h.config or a nil h.cond before
// Init has run — which is what actually happens in production, since
// mochi-mqtt's AddHook calls SetOpts before Init.
func (h *PriorityMessagingHook) maybeStart() {
	h.startMu.Lock()
	ready := h.configReady && h.serverReady && h.server != nil && !h.started
	if ready {
		h.started = true
	}
	h.startMu.Unlock()

	if ready {
		go h.schedulerLoop()
		go h.agingLoop()
	}
}

// SetOpts overrides HookBase.SetOpts to capture the Server reference.
func (h *PriorityMessagingHook) SetOpts(l *slog.Logger, o *mqtt.HookOptions) {
	h.HookBase.SetOpts(l, o)
	h.ensureQueues()
	h.server = o.Server

	h.startMu.Lock()
	h.serverReady = true
	h.startMu.Unlock()
	h.maybeStart()
}

// Stop halts the background scheduler.
func (h *PriorityMessagingHook) Stop() error {
	close(h.stop)
	h.cond.Broadcast()
	return nil
}

// OnPublish captures the packet, queues it by priority, and instructs the core to ignore it.
func (h *PriorityMessagingHook) OnPublish(cl *mqtt.Client, pk packets.Packet) (packets.Packet, error) {
	priority := PriorityNormal
	for _, p := range pk.Properties.User {
		if p.Key == "priority-processed" && p.Val == "true" {
			// Already processed by scheduler, let it pass through to normal routing
			return pk, nil
		}
		if p.Key == "priority" {
			priority = p.Val
		}
	}

	pp := prioritizedPacket{
		cl:       cl,
		pk:       pk,
		enqueued: time.Now(),
	}

	h.mu.Lock()
	switch priority {
	case PriorityUrgent:
		h.urgent = append(h.urgent, pp)
	case PriorityHigh:
		h.high = append(h.high, pp)
	default:
		h.normal = append(h.normal, pp)
	}
	h.mu.Unlock()
	h.cond.Signal()

	return pk, packets.CodeSuccessIgnore
}

// schedulerLoop continuously processes packets strictly by priority order.
func (h *PriorityMessagingHook) schedulerLoop() {
	for {
		h.mu.Lock()
		for len(h.urgent) == 0 && len(h.high) == 0 && len(h.normal) == 0 {
			select {
			case <-h.stop:
				h.mu.Unlock()
				return
			default:
			}
			h.cond.Wait()
		}

		select {
		case <-h.stop:
			h.mu.Unlock()
			return
		default:
		}

		var pp prioritizedPacket
		if len(h.urgent) > 0 {
			pp = h.urgent[0]
			h.urgent = h.urgent[1:]
		} else if len(h.high) > 0 {
			pp = h.high[0]
			h.high = h.high[1:]
		} else if len(h.normal) > 0 {
			pp = h.normal[0]
			h.normal = h.normal[1:]
		}
		h.mu.Unlock()

		if h.server != nil {
			// Mark it as processed so it doesn't get queued again!
			pp.pk.Properties.User = append(pp.pk.Properties.User, packets.UserProperty{
				Key: "priority-processed",
				Val: "true",
			})
			h.server.InjectPublish(pp.cl, pp.pk)
		}
	}
}

// agingLoop periodically scans High and Normal queues and promotes starved packets.
func (h *PriorityMessagingHook) agingLoop() {
	ticker := time.NewTicker(h.config.SchedulerInterval)
	defer ticker.Stop()

	for {
		select {
		case <-h.stop:
			return
		case now := <-ticker.C:
			h.mu.Lock()
			
			// Promote starved Normal packets to High (or directly to Urgent depending on age)
			var keepNormal []prioritizedPacket
			for _, pp := range h.normal {
				if now.Sub(pp.enqueued) >= h.config.AgingThresholdNormal {
					h.urgent = append(h.urgent, pp)
				} else {
					keepNormal = append(keepNormal, pp)
				}
			}
			h.normal = keepNormal

			// Promote starved High packets to Urgent
			var keepHigh []prioritizedPacket
			for _, pp := range h.high {
				if now.Sub(pp.enqueued) >= h.config.AgingThresholdHigh {
					h.urgent = append(h.urgent, pp)
				} else {
					keepHigh = append(keepHigh, pp)
				}
			}
			h.high = keepHigh

			// Wake up scheduler if we promoted anything
			if len(h.urgent) > 0 {
				h.cond.Signal()
			}
			h.mu.Unlock()
		}
	}
}
