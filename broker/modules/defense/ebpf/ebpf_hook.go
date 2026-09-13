package ebpf

import (
	"bytes"
	"log/slog"
	"net"
	"sync"
	"time"

	"errors"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
)

var (
	// ErrRejectConnect is returned when an IP is blocked due to connection floods.
	ErrRejectConnect = errors.New("connection rejected by eBPF filter")
)

// Options holds configuration for the EBPF Hook.
type Options struct {
	MaxConnectionsPerSecond int
}

// EBPFFilterHook tracks connection rates and bans IPs at the kernel level if they exceed the threshold.
type EBPFFilterHook struct {
	mqtt.HookBase
	log     *slog.Logger
	backend Backend
	options *Options

	mu        sync.Mutex
	connRates map[string]*rateTracker
}

type rateTracker struct {
	count     int
	firstSeen time.Time
	banned    bool
}

// ID returns the id of the hook.
func (h *EBPFFilterHook) ID() string {
	return "ebpf-filter"
}

// Provides indicates the hooks this module provides.
func (h *EBPFFilterHook) Provides(b byte) bool {
	return bytes.Contains([]byte{
		mqtt.OnConnect,
	}, []byte{b})
}

// Init initializes the eBPF hook.
func (h *EBPFFilterHook) Init(config any) error {
	h.log = h.Log.With("hook", "ebpf-filter")
	if config == nil {
		config = &Options{MaxConnectionsPerSecond: 10}
	}
	h.options = config.(*Options)
	h.connRates = make(map[string]*rateTracker)
	h.backend = NewBackend()

	// Use generic initialization for network interface.
	// In production, this might be "eth0" or "lo".
	if err := h.backend.Init(h.log, "lo"); err != nil {
		h.log.Error("failed to initialize eBPF backend", "error", err)
		// We log an error but don't fail Init to allow the broker to start even if eBPF privileges are lacking.
	}

	return nil
}

// OnConnect gets called when a client attempts to connect.
// We track their IP and ban them via eBPF if they connect too fast.
func (h *EBPFFilterHook) OnConnect(cl *mqtt.Client, pk packets.Packet) error {
	ipStr, _, err := net.SplitHostPort(cl.Net.Remote)
	if err != nil {
		// Cannot parse IP, likely not TCP/QUIC, fallback to raw remote.
		ipStr = cl.Net.Remote
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	tracker, ok := h.connRates[ipStr]
	if !ok {
		tracker = &rateTracker{
			count:     1,
			firstSeen: time.Now(),
		}
		h.connRates[ipStr] = tracker
		return nil
	}

	if tracker.banned {
		return ErrRejectConnect
	}

	now := time.Now()
	// Reset the tracker if a second has passed
	if now.Sub(tracker.firstSeen) >= time.Second {
		tracker.count = 1
		tracker.firstSeen = now
		return nil
	}

	tracker.count++
	if tracker.count > h.options.MaxConnectionsPerSecond {
		tracker.banned = true
		h.log.Warn("connection flood detected, banning IP", "ip", ipStr, "rate", tracker.count)
		_ = h.backend.BlockIP(ipStr)
		return ErrRejectConnect
	}

	return nil
}
