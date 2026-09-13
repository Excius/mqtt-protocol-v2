package routing

import (
	"log/slog"
	"testing"
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
)

func TestPriorityMessagingHook_Init(t *testing.T) {
	h := new(PriorityMessagingHook)

	err := h.Init(nil)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if h.config.AgingThresholdHigh != 500*time.Millisecond {
		t.Errorf("expected default High threshold")
	}

	cfg := &PriorityMessagingConfig{
		AgingThresholdHigh:   100 * time.Millisecond,
		AgingThresholdNormal: 200 * time.Millisecond,
		SchedulerInterval:    5 * time.Millisecond,
	}

	err = h.Init(cfg)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if h.config.AgingThresholdHigh != 100*time.Millisecond {
		t.Errorf("expected configured High threshold")
	}

	err = h.Init("invalid")
	if err != mqtt.ErrInvalidConfigType {
		t.Errorf("expected ErrInvalidConfigType, got %v", err)
	}
}

func TestPriorityMessagingHook_Ordering(t *testing.T) {
	h := new(PriorityMessagingHook)
	_ = h.Init(nil)

	pkNormal := packets.Packet{
		PacketID: 1,
		Properties: packets.Properties{
			User: []packets.UserProperty{{Key: "priority", Val: "Normal"}},
		},
	}
	pkUrgent := packets.Packet{
		PacketID: 2,
		Properties: packets.Properties{
			User: []packets.UserProperty{{Key: "priority", Val: "Urgent"}},
		},
	}
	pkHigh := packets.Packet{
		PacketID: 3,
		Properties: packets.Properties{
			User: []packets.UserProperty{{Key: "priority", Val: "High"}},
		},
	}

	_, err1 := h.OnPublish(nil, pkNormal)
	_, err2 := h.OnPublish(nil, pkHigh)
	_, err3 := h.OnPublish(nil, pkUrgent)

	if err1 != packets.CodeSuccessIgnore || err2 != packets.CodeSuccessIgnore || err3 != packets.CodeSuccessIgnore {
		t.Fatalf("expected all to return CodeSuccessIgnore")
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if len(h.urgent) != 1 || len(h.high) != 1 || len(h.normal) != 1 {
		t.Fatalf("expected 1 packet in each queue")
	}

	if h.urgent[0].pk.PacketID != 2 {
		t.Errorf("expected Urgent packet in urgent queue")
	}
}

// TestPriorityMessagingHook_SetOptsBeforeInit is a regression test for a race
// where mochi-mqtt's AddHook calls SetOpts (which starts the scheduler/aging
// goroutines) before it calls Init (which used to allocate h.cond and
// h.stop). If those goroutines observed a nil h.cond before Init ran, they
// would panic. ensureQueues() must make this ordering safe.
func TestPriorityMessagingHook_SetOptsBeforeInit(t *testing.T) {
	h := new(PriorityMessagingHook)
	server := mqtt.New(nil)

	// Mirrors server.go AddHook: SetOpts is called before Init.
	h.SetOpts(slog.Default(), &mqtt.HookOptions{Server: server})
	if err := h.Init(nil); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	pk := packets.Packet{
		Properties: packets.Properties{
			User: []packets.UserProperty{{Key: "priority", Val: "Urgent"}},
		},
	}
	if _, err := h.OnPublish(nil, pk); err != packets.CodeSuccessIgnore {
		t.Fatalf("expected CodeSuccessIgnore, got %v", err)
	}

	if err := h.Stop(); err != nil {
		t.Fatalf("expected clean stop, got %v", err)
	}
}

func TestPriorityMessagingHook_Aging(t *testing.T) {
	cfg := &PriorityMessagingConfig{
		AgingThresholdHigh:   10 * time.Millisecond,
		AgingThresholdNormal: 20 * time.Millisecond,
		SchedulerInterval:    1 * time.Millisecond,
	}

	h := new(PriorityMessagingHook)
	_ = h.Init(cfg)

	// manually enqueue packets with old enqueued times
	now := time.Now()
	h.normal = append(h.normal, prioritizedPacket{
		pk:       packets.Packet{PacketID: 1},
		enqueued: now.Add(-30 * time.Millisecond), // starved!
	})
	h.high = append(h.high, prioritizedPacket{
		pk:       packets.Packet{PacketID: 2},
		enqueued: now.Add(-15 * time.Millisecond), // starved!
	})
	h.normal = append(h.normal, prioritizedPacket{
		pk:       packets.Packet{PacketID: 3},
		enqueued: now, // fresh
	})

	// Start aging loop
	go h.agingLoop()

	time.Sleep(10 * time.Millisecond) // wait for aging loop to process

	_ = h.Stop()

	h.mu.Lock()
	defer h.mu.Unlock()

	if len(h.urgent) != 2 {
		t.Errorf("expected 2 packets promoted to urgent, got %d", len(h.urgent))
	}
	if len(h.high) != 0 {
		t.Errorf("expected 0 packets in high, got %d", len(h.high))
	}
	if len(h.normal) != 1 {
		t.Errorf("expected 1 packet in normal, got %d", len(h.normal))
	}
	if len(h.normal) > 0 && h.normal[0].pk.PacketID != 3 {
		t.Errorf("expected fresh packet to remain in normal")
	}
}
