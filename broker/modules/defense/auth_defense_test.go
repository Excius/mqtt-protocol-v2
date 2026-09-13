package defense

import (
	"testing"
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
)

func newTestClientWithAddr(id, remote string) *mqtt.Client {
	return &mqtt.Client{
		ID:  id,
		Net: mqtt.ClientConnection{Remote: remote},
	}
}

func TestAuthDefenseHook_ID(t *testing.T) {
	h := &AuthDefenseHook{}
	if h.ID() != "auth-defense" {
		t.Fatalf("expected ID 'auth-defense', got %q", h.ID())
	}
}

func TestAuthDefenseHook_Provides(t *testing.T) {
	h := &AuthDefenseHook{}
	for _, b := range []byte{mqtt.OnConnect, mqtt.OnDisconnect, mqtt.OnAuthPacket, mqtt.OnSessionEstablished} {
		if !h.Provides(b) {
			t.Fatalf("expected hook to provide byte %d", b)
		}
	}
	if h.Provides(mqtt.OnPublish) {
		t.Fatal("hook should not provide OnPublish")
	}
}

func TestAuthDefenseHook_InitDefaults(t *testing.T) {
	h := &AuthDefenseHook{}
	if err := h.Init(nil); err != nil {
		t.Fatalf("Init with nil config should not error: %v", err)
	}
	if h.config.MaxConnPerSec != DefaultMaxConnPerSec {
		t.Fatalf("expected MaxConnPerSec=%d, got %d", DefaultMaxConnPerSec, h.config.MaxConnPerSec)
	}
	if h.config.MaxConcurrentConn != DefaultMaxConcurrentConn {
		t.Fatalf("expected MaxConcurrentConn=%d, got %d", DefaultMaxConcurrentConn, h.config.MaxConcurrentConn)
	}
	if h.config.MaxAuthPerConn != DefaultMaxAuthPerConn {
		t.Fatalf("expected MaxAuthPerConn=%d, got %d", DefaultMaxAuthPerConn, h.config.MaxAuthPerConn)
	}
	if h.config.ConnTimeout != DefaultConnTimeout {
		t.Fatalf("expected ConnTimeout=%v, got %v", DefaultConnTimeout, h.config.ConnTimeout)
	}
}

func TestAuthDefenseHook_InitCustomConfig(t *testing.T) {
	h := &AuthDefenseHook{}
	cfg := &AuthDefenseConfig{
		MaxConnPerSec:     2,
		MaxConcurrentConn: 3,
		MaxAuthPerConn:    1,
		ConnTimeout:       50 * time.Millisecond,
	}
	if err := h.Init(cfg); err != nil {
		t.Fatalf("Init with valid config should not error: %v", err)
	}
	if h.config.MaxConnPerSec != 2 || h.config.MaxConcurrentConn != 3 || h.config.MaxAuthPerConn != 1 || h.config.ConnTimeout != 50*time.Millisecond {
		t.Fatalf("config not applied as provided: %+v", h.config)
	}
}

func TestAuthDefenseHook_InitZeroValuesFallBackToDefaults(t *testing.T) {
	h := &AuthDefenseHook{}
	if err := h.Init(&AuthDefenseConfig{}); err != nil {
		t.Fatalf("Init with zero-value config should not error: %v", err)
	}
	if h.config.MaxConnPerSec != DefaultMaxConnPerSec {
		t.Fatalf("expected fallback MaxConnPerSec=%d, got %d", DefaultMaxConnPerSec, h.config.MaxConnPerSec)
	}
	if h.config.MaxConcurrentConn != DefaultMaxConcurrentConn {
		t.Fatalf("expected fallback MaxConcurrentConn=%d, got %d", DefaultMaxConcurrentConn, h.config.MaxConcurrentConn)
	}
	if h.config.MaxAuthPerConn != DefaultMaxAuthPerConn {
		t.Fatalf("expected fallback MaxAuthPerConn=%d, got %d", DefaultMaxAuthPerConn, h.config.MaxAuthPerConn)
	}
	if h.config.ConnTimeout != DefaultConnTimeout {
		t.Fatalf("expected fallback ConnTimeout=%v, got %v", DefaultConnTimeout, h.config.ConnTimeout)
	}
}

func TestAuthDefenseHook_InitInvalidConfig(t *testing.T) {
	h := &AuthDefenseHook{}
	if err := h.Init("invalid"); err == nil {
		t.Fatal("Init with invalid config type should return error")
	}
}

func TestAuthDefenseHook_OnConnect_AllowsWithinLimits(t *testing.T) {
	h := &AuthDefenseHook{}
	_ = h.Init(&AuthDefenseConfig{MaxConcurrentConn: 5, MaxConnPerSec: 5, ConnTimeout: time.Second})
	cl := newTestClientWithAddr("client-1", "10.0.0.1:1000")
	if err := h.OnConnect(cl, packets.Packet{}); err != nil {
		t.Fatalf("expected connection within limits to be allowed: %v", err)
	}
	h.OnDisconnect(cl, nil, false)
}

func TestAuthDefenseHook_OnConnect_RejectsMaxConcurrentConnections(t *testing.T) {
	h := &AuthDefenseHook{}
	_ = h.Init(&AuthDefenseConfig{MaxConcurrentConn: 2, MaxConnPerSec: 1000, ConnTimeout: time.Second})

	cl1 := newTestClientWithAddr("client-1", "10.0.0.1:1001")
	cl2 := newTestClientWithAddr("client-2", "10.0.0.2:1002")
	cl3 := newTestClientWithAddr("client-3", "10.0.0.3:1003")

	if err := h.OnConnect(cl1, packets.Packet{}); err != nil {
		t.Fatalf("connection 1 should be allowed: %v", err)
	}
	if err := h.OnConnect(cl2, packets.Packet{}); err != nil {
		t.Fatalf("connection 2 should be allowed: %v", err)
	}
	if err := h.OnConnect(cl3, packets.Packet{}); err == nil {
		t.Fatal("connection 3 should be rejected: max concurrent connections exceeded")
	}

	_, _, connRejected, _ := h.Metrics()
	if connRejected != 1 {
		t.Fatalf("expected 1 rejected connection, got %d", connRejected)
	}

	// Freeing a slot via disconnect should allow a new connection in.
	h.OnDisconnect(cl1, nil, false)
	cl4 := newTestClientWithAddr("client-4", "10.0.0.4:1004")
	if err := h.OnConnect(cl4, packets.Packet{}); err != nil {
		t.Fatalf("connection 4 should be allowed after a slot frees up: %v", err)
	}
}

func TestAuthDefenseHook_OnConnect_RejectsPerIPRateLimit(t *testing.T) {
	h := &AuthDefenseHook{}
	_ = h.Init(&AuthDefenseConfig{MaxConcurrentConn: 1000, MaxConnPerSec: 2, ConnTimeout: time.Second})

	sameIP := "10.0.0.9"
	cl1 := newTestClientWithAddr("client-1", sameIP+":1001")
	cl2 := newTestClientWithAddr("client-2", sameIP+":1002")
	cl3 := newTestClientWithAddr("client-3", sameIP+":1003")

	if err := h.OnConnect(cl1, packets.Packet{}); err != nil {
		t.Fatalf("connection 1 from IP should be allowed: %v", err)
	}
	if err := h.OnConnect(cl2, packets.Packet{}); err != nil {
		t.Fatalf("connection 2 from IP should be allowed: %v", err)
	}
	if err := h.OnConnect(cl3, packets.Packet{}); err == nil {
		t.Fatal("connection 3 from same IP within one second should be rate limited")
	}

	// A different source IP should not be affected by the first IP's rate limit.
	clOther := newTestClientWithAddr("client-other", "10.0.0.10:2000")
	if err := h.OnConnect(clOther, packets.Packet{}); err != nil {
		t.Fatalf("connection from a different IP should be allowed: %v", err)
	}
}

func TestAuthDefenseHook_OnConnect_RateLimitResetsAfterWindow(t *testing.T) {
	h := &AuthDefenseHook{}
	_ = h.Init(&AuthDefenseConfig{MaxConcurrentConn: 1000, MaxConnPerSec: 1, ConnTimeout: time.Second})

	ip := "10.0.0.20"
	cl1 := newTestClientWithAddr("client-1", ip+":3000")
	if err := h.OnConnect(cl1, packets.Packet{}); err != nil {
		t.Fatalf("first connection should be allowed: %v", err)
	}

	cl2 := newTestClientWithAddr("client-2", ip+":3001")
	if err := h.OnConnect(cl2, packets.Packet{}); err == nil {
		t.Fatal("second connection within the same window should be rate limited")
	}

	time.Sleep(1100 * time.Millisecond)

	cl3 := newTestClientWithAddr("client-3", ip+":3002")
	if err := h.OnConnect(cl3, packets.Packet{}); err != nil {
		t.Fatalf("connection after the rate limit window resets should be allowed: %v", err)
	}
}

func TestAuthDefenseHook_OnAuthPacket_AllowsWithinLimit(t *testing.T) {
	h := &AuthDefenseHook{}
	_ = h.Init(&AuthDefenseConfig{MaxConcurrentConn: 10, MaxConnPerSec: 10, MaxAuthPerConn: 2, ConnTimeout: time.Second})
	cl := newTestClientWithAddr("client-1", "10.0.1.1:4000")
	_ = h.OnConnect(cl, packets.Packet{})

	if _, err := h.OnAuthPacket(cl, packets.Packet{}); err != nil {
		t.Fatalf("first AUTH packet should be allowed: %v", err)
	}
	if _, err := h.OnAuthPacket(cl, packets.Packet{}); err != nil {
		t.Fatalf("second AUTH packet (at limit) should be allowed: %v", err)
	}

	received, blocked, _, _ := h.Metrics()
	if received != 2 {
		t.Fatalf("expected 2 AUTH packets received, got %d", received)
	}
	if blocked != 0 {
		t.Fatalf("expected 0 AUTH packets blocked, got %d", blocked)
	}
}

func TestAuthDefenseHook_OnAuthPacket_RejectsExceedingLimit(t *testing.T) {
	h := &AuthDefenseHook{}
	_ = h.Init(&AuthDefenseConfig{MaxConcurrentConn: 10, MaxConnPerSec: 10, MaxAuthPerConn: 1, ConnTimeout: time.Second})
	cl := newTestClientWithAddr("client-1", "10.0.1.2:4001")
	_ = h.OnConnect(cl, packets.Packet{})

	if _, err := h.OnAuthPacket(cl, packets.Packet{}); err != nil {
		t.Fatalf("first AUTH packet should be allowed: %v", err)
	}
	if _, err := h.OnAuthPacket(cl, packets.Packet{}); err != packets.ErrRejectPacket {
		t.Fatalf("second AUTH packet exceeding limit should be rejected, got %v", err)
	}

	_, blocked, _, violations := h.Metrics()
	if blocked != 1 {
		t.Fatalf("expected 1 AUTH packet blocked, got %d", blocked)
	}
	if violations != 1 {
		t.Fatalf("expected 1 auth violation, got %d", violations)
	}
}

func TestAuthDefenseHook_OnAuthPacket_RejectsWithoutConnectState(t *testing.T) {
	h := &AuthDefenseHook{}
	_ = h.Init(nil)
	// A client that never went through OnConnect (e.g. state missing) must be blocked.
	cl := newTestClientWithAddr("ghost-client", "10.0.1.3:4002")

	if _, err := h.OnAuthPacket(cl, packets.Packet{}); err != packets.ErrRejectPacket {
		t.Fatalf("AUTH packet without prior connect state should be rejected, got %v", err)
	}
}

func TestAuthDefenseHook_OnSessionEstablished_PreventsAuthTimeout(t *testing.T) {
	h := &AuthDefenseHook{}
	_ = h.Init(&AuthDefenseConfig{MaxConcurrentConn: 10, MaxConnPerSec: 10, MaxAuthPerConn: 5, ConnTimeout: 30 * time.Millisecond})
	cl := newTestClientWithAddr("client-1", "10.0.2.1:5000")

	_ = h.OnConnect(cl, packets.Packet{})
	h.OnSessionEstablished(cl, packets.Packet{})

	// Wait past ConnTimeout: since the session was established, the timer
	// must have been stopped and no auth-timeout violation should fire.
	time.Sleep(80 * time.Millisecond)

	_, _, _, violations := h.Metrics()
	if violations != 0 {
		t.Fatalf("expected 0 auth violations after session established, got %d", violations)
	}
	if cl.StopCause() != nil {
		t.Fatalf("client should not have been stopped, got cause: %v", cl.StopCause())
	}
}

func TestAuthDefenseHook_AuthTimeout_StopsUnauthenticatedClient(t *testing.T) {
	h := &AuthDefenseHook{}
	_ = h.Init(&AuthDefenseConfig{MaxConcurrentConn: 10, MaxConnPerSec: 10, MaxAuthPerConn: 5, ConnTimeout: 20 * time.Millisecond})
	cl := newTestClientWithAddr("client-1", "10.0.2.2:5001")

	_ = h.OnConnect(cl, packets.Packet{})
	// Never call OnSessionEstablished: the timeout should fire and stop the client.
	time.Sleep(100 * time.Millisecond)

	if cl.StopCause() == nil {
		t.Fatal("expected client to be stopped after auth timeout")
	}

	_, _, _, violations := h.Metrics()
	if violations != 1 {
		t.Fatalf("expected 1 auth violation from timeout, got %d", violations)
	}
}

func TestAuthDefenseHook_OnDisconnect_CleansUpStateAndStopsTimer(t *testing.T) {
	h := &AuthDefenseHook{}
	_ = h.Init(&AuthDefenseConfig{MaxConcurrentConn: 10, MaxConnPerSec: 10, MaxAuthPerConn: 5, ConnTimeout: 20 * time.Millisecond})
	cl := newTestClientWithAddr("client-1", "10.0.2.3:5002")

	_ = h.OnConnect(cl, packets.Packet{})
	h.OnDisconnect(cl, nil, false)

	if _, ok := h.clients.Load(cl.ID); ok {
		t.Fatal("expected client state to be removed after disconnect")
	}

	// Wait past ConnTimeout to confirm the timer was stopped and does not
	// fire a spurious violation for an already-disconnected client.
	time.Sleep(80 * time.Millisecond)
	_, _, _, violations := h.Metrics()
	if violations != 0 {
		t.Fatalf("expected 0 auth violations after clean disconnect, got %d", violations)
	}
}

func TestAuthDefenseHook_Metrics(t *testing.T) {
	h := &AuthDefenseHook{}
	_ = h.Init(&AuthDefenseConfig{MaxConcurrentConn: 1, MaxConnPerSec: 1, MaxAuthPerConn: 1, ConnTimeout: time.Second})

	cl1 := newTestClientWithAddr("client-1", "10.0.3.1:6000")
	cl2 := newTestClientWithAddr("client-2", "10.0.3.2:6001")

	_ = h.OnConnect(cl1, packets.Packet{})
	_ = h.OnConnect(cl2, packets.Packet{}) // rejected: exceeds MaxConcurrentConn

	_, _ = h.OnAuthPacket(cl1, packets.Packet{})
	_, _ = h.OnAuthPacket(cl1, packets.Packet{}) // rejected: exceeds MaxAuthPerConn

	received, blocked, connRejected, violations := h.Metrics()
	if received != 2 {
		t.Fatalf("expected 2 auth packets received, got %d", received)
	}
	if blocked != 1 {
		t.Fatalf("expected 1 auth packet blocked, got %d", blocked)
	}
	if connRejected != 1 {
		t.Fatalf("expected 1 connection rejected, got %d", connRejected)
	}
	if violations != 1 {
		t.Fatalf("expected 1 auth violation, got %d", violations)
	}
}

func TestGetIP(t *testing.T) {
	cases := map[string]string{
		"192.168.1.1:1234": "192.168.1.1",
		"[::1]:8080":        "::1",
		"no-port-here":      "no-port-here",
	}
	for addr, want := range cases {
		if got := getIP(addr); got != want {
			t.Fatalf("getIP(%q) = %q, want %q", addr, got, want)
		}
	}
}
