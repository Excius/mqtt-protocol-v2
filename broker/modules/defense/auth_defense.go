package defense

import (
	"bytes"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
)

// Default limits for authentication flood mitigation
const (
	DefaultMaxConnPerSec     = 5
	DefaultMaxConcurrentConn = 20
	DefaultMaxAuthPerConn    = 2
	DefaultConnTimeout       = 30 * time.Second
)

// AuthDefenseConfig holds configuration for the auth defense hook.
type AuthDefenseConfig struct {
	MaxConnPerSec     int           // Max new connections per IP per second
	MaxConcurrentConn int           // Max total concurrent connections allowed across the broker
	MaxAuthPerConn    int           // Max AUTH packets allowed per session
	ConnTimeout       time.Duration // Maximum time allowed to complete authentication
}

type ipTracker struct {
	mu        sync.Mutex
	count     int
	resetTime time.Time
}

type clientState struct {
	authCount int32
	timer     *time.Timer
	isAuthed  int32
}

// AuthDefenseHook mitigates authentication flood and slowloris attacks.
type AuthDefenseHook struct {
	mqtt.HookBase
	config AuthDefenseConfig

	// Concurrency tracking
	activeConnections int64

	// State tracking
	// State tracking
	ipRates sync.Map
	clients sync.Map

	// Metrics (atomic counters)
	AuthPacketsReceived int64
	AuthPacketsBlocked  int64
	ConnectionsRejected int64
	AuthViolationCount  int64
}

// ID returns the unique identifier for this hook.
func (h *AuthDefenseHook) ID() string {
	return "auth-defense"
}

// Provides indicates which hook methods this hook implements.
func (h *AuthDefenseHook) Provides(b byte) bool {
	return bytes.Contains([]byte{
		mqtt.OnConnect,
		mqtt.OnDisconnect,
		mqtt.OnAuthPacket,
		mqtt.OnSessionEstablished,
	}, []byte{b})
}

// Init initializes the hook.
func (h *AuthDefenseHook) Init(config any) error {
	if config == nil {
		h.config = AuthDefenseConfig{
			MaxConnPerSec:     DefaultMaxConnPerSec,
			MaxConcurrentConn: DefaultMaxConcurrentConn,
			MaxAuthPerConn:    DefaultMaxAuthPerConn,
			ConnTimeout:       DefaultConnTimeout,
		}
		return nil
	}

	cfg, ok := config.(*AuthDefenseConfig)
	if !ok {
		return mqtt.ErrInvalidConfigType
	}

	h.config = *cfg

	if h.config.MaxConnPerSec <= 0 {
		h.config.MaxConnPerSec = DefaultMaxConnPerSec
	}
	if h.config.MaxConcurrentConn <= 0 {
		h.config.MaxConcurrentConn = DefaultMaxConcurrentConn
	}
	if h.config.MaxAuthPerConn <= 0 {
		h.config.MaxAuthPerConn = DefaultMaxAuthPerConn
	}
	if h.config.ConnTimeout <= 0 {
		h.config.ConnTimeout = DefaultConnTimeout
	}

	return nil
}

// OnConnect handles new connections, enforcing limits and starting the auth timer.
func (h *AuthDefenseHook) OnConnect(cl *mqtt.Client, pk packets.Packet) error {
	// 1. Check max concurrent connections
	current := atomic.AddInt64(&h.activeConnections, 1)
	if current > int64(h.config.MaxConcurrentConn) {
		atomic.AddInt64(&h.activeConnections, -1) // revert
		h.recordConnViolation(cl, "max_concurrent_connections_exceeded")
		return errors.New("server too busy")
	}

	// Extract IP from Remote Addr
	ip := getIP(cl.Net.Remote)

	// 2. Check per-IP connection rate
	now := time.Now()
	val, _ := h.ipRates.LoadOrStore(ip, &ipTracker{count: 0, resetTime: now.Add(time.Second)})
	tracker := val.(*ipTracker)

	// We use atomic operations if possible or just accept a tiny race condition
	// For exactness, we can use a local mutex per tracker or just lock-free approx
	// Let's use simple locking per tracker to avoid global mutex contention
	tracker.mu.Lock()
	if now.After(tracker.resetTime) {
		tracker.count = 0
		tracker.resetTime = now.Add(time.Second)
	}
	tracker.count++
	count := tracker.count
	tracker.mu.Unlock()

	if count > h.config.MaxConnPerSec {
		atomic.AddInt64(&h.activeConnections, -1) // revert
		h.recordConnViolation(cl, "ip_rate_limit_exceeded")
		return errors.New("rate limit exceeded")
	}

	// 3. Setup client state and auth timeout timer
	state := &clientState{
		authCount: 0,
		isAuthed:  0, // use atomic int32 instead of bool
	}

	clientID := cl.ID
	state.timer = time.AfterFunc(h.config.ConnTimeout, func() {
		stVal, ok := h.clients.Load(clientID)
		if ok {
			st := stVal.(*clientState)
			if atomic.LoadInt32(&st.isAuthed) == 0 {
				h.recordAuthViolation(cl, "auth_timeout")
				cl.Stop(errors.New("authentication timeout"))
			}
		}
	})

	h.clients.Store(clientID, state)

	return nil
}

// OnDisconnect cleans up tracked connection data.
func (h *AuthDefenseHook) OnDisconnect(cl *mqtt.Client, err error, expire bool) {
	atomic.AddInt64(&h.activeConnections, -1)

	if stVal, ok := h.clients.LoadAndDelete(cl.ID); ok {
		state := stVal.(*clientState)
		if state.timer != nil {
			state.timer.Stop()
		}
	}
}

// OnAuthPacket limits the number of AUTH packets a single client can send during enhanced auth.
func (h *AuthDefenseHook) OnAuthPacket(cl *mqtt.Client, pk packets.Packet) (packets.Packet, error) {
	atomic.AddInt64(&h.AuthPacketsReceived, 1)

	stVal, ok := h.clients.Load(cl.ID)
	if !ok {
		// If they bypass Connect somehow or state is missing, block the packet
		atomic.AddInt64(&h.AuthPacketsBlocked, 1)
		return pk, packets.ErrRejectPacket
	}

	state := stVal.(*clientState)
	count := atomic.AddInt32((*int32)(&state.authCount), 1)
	if count > int32(h.config.MaxAuthPerConn) {
		h.recordAuthViolation(cl, "max_auth_packets_exceeded")
		return pk, packets.ErrRejectPacket
	}

	return pk, nil
}

// OnSessionEstablished marks the client as authenticated, preventing the timeout.
func (h *AuthDefenseHook) OnSessionEstablished(cl *mqtt.Client, pk packets.Packet) {
	if stVal, ok := h.clients.Load(cl.ID); ok {
		state := stVal.(*clientState)
		atomic.StoreInt32(&state.isAuthed, 1)
		if state.timer != nil {
			state.timer.Stop()
		}
	}
}

// Helper methods for metrics and logging
func (h *AuthDefenseHook) recordConnViolation(cl *mqtt.Client, reason string) {
	atomic.AddInt64(&h.ConnectionsRejected, 1)
	if h.Log != nil {
		h.Log.Debug("connection rejected", "client", cl.ID, "remote", cl.Net.Remote, "reason", reason)
	}
}

func (h *AuthDefenseHook) recordAuthViolation(cl *mqtt.Client, reason string) {
	atomic.AddInt64(&h.AuthPacketsBlocked, 1)
	atomic.AddInt64(&h.AuthViolationCount, 1)
	if h.Log != nil {
		h.Log.Debug("auth violation", "client", cl.ID, "remote", cl.Net.Remote, "reason", reason)
	}
}

// getIP strips the port from a network address (e.g. 192.168.1.1:1234 -> 192.168.1.1)
func getIP(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		if strings.Contains(err.Error(), "missing port in address") {
			return addr
		}
		return "unknown"
	}
	return host
}

// Metrics returns the current metrics snapshot.
func (h *AuthDefenseHook) Metrics() (authReceived, authBlocked, connRejected, authViolations int64) {
	return atomic.LoadInt64(&h.AuthPacketsReceived),
		atomic.LoadInt64(&h.AuthPacketsBlocked),
		atomic.LoadInt64(&h.ConnectionsRejected),
		atomic.LoadInt64(&h.AuthViolationCount)
}
