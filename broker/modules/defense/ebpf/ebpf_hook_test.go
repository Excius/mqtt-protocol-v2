package ebpf

import (
	"log/slog"
	"testing"
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
	"github.com/stretchr/testify/require"
)

// mockBackend is a stub for tests to verify if an IP was blocked.
type mockBackend struct {
	blockedIP string
}

func (m *mockBackend) Init(log *slog.Logger, ifaceName string) error { return nil }
func (m *mockBackend) BlockIP(ip string) error {
	m.blockedIP = ip
	return nil
}
func (m *mockBackend) Close() error { return nil }

func TestEBPFFilterHook_OnConnect(t *testing.T) {
	h := new(EBPFFilterHook)
	h.Log = slog.Default()
	
	err := h.Init(&Options{MaxConnectionsPerSecond: 2})
	require.NoError(t, err)

	// Inject the mock backend
	mock := &mockBackend{}
	h.backend = mock

	cl1 := &mqtt.Client{
		Net: mqtt.ClientConnection{
			Remote: "192.168.1.100:12345",
		},
	}
	pk := packets.Packet{}

	// Connection 1: allowed
	err = h.OnConnect(cl1, pk)
	require.NoError(t, err)
	require.Equal(t, "", mock.blockedIP)

	// Connection 2: allowed
	err = h.OnConnect(cl1, pk)
	require.NoError(t, err)
	require.Equal(t, "", mock.blockedIP)

	// Connection 3: should be blocked
	err = h.OnConnect(cl1, pk)
	require.ErrorIs(t, err, ErrRejectConnect)
	require.Equal(t, "192.168.1.100", mock.blockedIP)

	// A ban is sticky: once tracker.banned is set, OnConnect keeps rejecting
	// the IP even after the 1-second rate limit window has elapsed.
	time.Sleep(1100 * time.Millisecond)

	err = h.OnConnect(cl1, pk)
	require.ErrorIs(t, err, ErrRejectConnect)
}

func TestEBPFFilterHook_Provides(t *testing.T) {
	h := new(EBPFFilterHook)
	require.True(t, h.Provides(mqtt.OnConnect))
	require.False(t, h.Provides(mqtt.OnDisconnect))
}

func TestEBPFFilterHook_ID(t *testing.T) {
	h := new(EBPFFilterHook)
	require.Equal(t, "ebpf-filter", h.ID())
}
