package defense

import (
	"strings"
	"testing"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
)

func newTestClient(id string) *mqtt.Client {
	return &mqtt.Client{
		ID: id,
	}
}

func TestPropertyValidatorHook_ID(t *testing.T) {
	h := &PropertyValidatorHook{}
	if h.ID() != "user-property-validator" {
		t.Fatalf("expected ID 'user-property-validator', got %q", h.ID())
	}
}

func TestPropertyValidatorHook_Provides(t *testing.T) {
	h := &PropertyValidatorHook{}
	if !h.Provides(mqtt.OnPublish) {
		t.Fatal("expected hook to provide OnPublish")
	}
	if !h.Provides(mqtt.OnDisconnect) {
		t.Fatal("expected hook to provide OnDisconnect")
	}
	if h.Provides(mqtt.OnConnect) {
		t.Fatal("hook should not provide OnConnect")
	}
}

func TestPropertyValidatorHook_InitDefaults(t *testing.T) {
	h := &PropertyValidatorHook{}
	if err := h.Init(nil); err != nil {
		t.Fatalf("Init with nil config should not error: %v", err)
	}
	if h.config.MaxProperties != DefaultMaxProperties {
		t.Fatalf("expected MaxProperties=%d, got %d", DefaultMaxProperties, h.config.MaxProperties)
	}
	if h.config.MaxClientBudget != DefaultMaxClientBudget {
		t.Fatalf("expected MaxClientBudget=%d, got %d", DefaultMaxClientBudget, h.config.MaxClientBudget)
	}
}

func TestPropertyValidatorHook_InitCustomConfig(t *testing.T) {
	h := &PropertyValidatorHook{}
	cfg := &PropertyValidatorConfig{
		MaxProperties:      5,
		MaxKeySize:         64,
		MaxValueSize:       128,
		MaxPropertyPayload: 1024,
		MaxClientBudget:    8192,
	}
	if err := h.Init(cfg); err != nil {
		t.Fatalf("Init with valid config should not error: %v", err)
	}
	if h.config.MaxProperties != 5 {
		t.Fatalf("expected MaxProperties=5, got %d", h.config.MaxProperties)
	}
}

func TestPropertyValidatorHook_InitInvalidConfig(t *testing.T) {
	h := &PropertyValidatorHook{}
	if err := h.Init("invalid"); err == nil {
		t.Fatal("Init with invalid config type should return error")
	}
}

func TestPropertyValidatorHook_AllowNormalMessage(t *testing.T) {
	h := &PropertyValidatorHook{}
	_ = h.Init(nil)
	cl := newTestClient("client-1")
	pk := packets.Packet{
		FixedHeader: packets.FixedHeader{Type: packets.Publish},
		Properties: packets.Properties{
			User: []packets.UserProperty{
				{Key: "key1", Val: "val1"},
				{Key: "key2", Val: "val2"},
			},
		},
	}
	_, err := h.OnPublish(cl, pk)
	if err != nil {
		t.Fatalf("normal message should be allowed: %v", err)
	}
	checked, dropped, _ := h.Metrics()
	if checked != 1 {
		t.Fatalf("expected 1 packet checked, got %d", checked)
	}
	if dropped != 0 {
		t.Fatalf("expected 0 packets dropped, got %d", dropped)
	}
}

func TestPropertyValidatorHook_AllowNoProperties(t *testing.T) {
	h := &PropertyValidatorHook{}
	_ = h.Init(nil)
	cl := newTestClient("client-2")
	pk := packets.Packet{
		FixedHeader: packets.FixedHeader{Type: packets.Publish},
	}
	_, err := h.OnPublish(cl, pk)
	if err != nil {
		t.Fatalf("message with no properties should be allowed: %v", err)
	}
}

func TestPropertyValidatorHook_RejectTooManyProperties(t *testing.T) {
	h := &PropertyValidatorHook{}
	_ = h.Init(&PropertyValidatorConfig{MaxProperties: 3})
	cl := newTestClient("client-3")
	pk := packets.Packet{
		FixedHeader: packets.FixedHeader{Type: packets.Publish},
		Properties: packets.Properties{
			User: []packets.UserProperty{
				{Key: "a", Val: "1"},
				{Key: "b", Val: "2"},
				{Key: "c", Val: "3"},
				{Key: "d", Val: "4"},
			},
		},
	}
	_, err := h.OnPublish(cl, pk)
	if err != packets.ErrRejectPacket {
		t.Fatalf("expected ErrRejectPacket for too many properties, got %v", err)
	}
	_, dropped, violations := h.Metrics()
	if dropped != 1 {
		t.Fatalf("expected 1 packet dropped, got %d", dropped)
	}
	if violations != 1 {
		t.Fatalf("expected 1 violation, got %d", violations)
	}
}

func TestPropertyValidatorHook_RejectOversizedKey(t *testing.T) {
	h := &PropertyValidatorHook{}
	_ = h.Init(&PropertyValidatorConfig{MaxKeySize: 10})
	cl := newTestClient("client-4")
	pk := packets.Packet{
		FixedHeader: packets.FixedHeader{Type: packets.Publish},
		Properties: packets.Properties{
			User: []packets.UserProperty{
				{Key: strings.Repeat("x", 11), Val: "ok"},
			},
		},
	}
	_, err := h.OnPublish(cl, pk)
	if err != packets.ErrRejectPacket {
		t.Fatalf("expected ErrRejectPacket for oversized key, got %v", err)
	}
}

func TestPropertyValidatorHook_RejectOversizedValue(t *testing.T) {
	h := &PropertyValidatorHook{}
	_ = h.Init(&PropertyValidatorConfig{MaxValueSize: 10})
	cl := newTestClient("client-5")
	pk := packets.Packet{
		FixedHeader: packets.FixedHeader{Type: packets.Publish},
		Properties: packets.Properties{
			User: []packets.UserProperty{
				{Key: "ok", Val: strings.Repeat("y", 11)},
			},
		},
	}
	_, err := h.OnPublish(cl, pk)
	if err != packets.ErrRejectPacket {
		t.Fatalf("expected ErrRejectPacket for oversized value, got %v", err)
	}
}

func TestPropertyValidatorHook_RejectExceedsPacketBudget(t *testing.T) {
	h := &PropertyValidatorHook{}
	_ = h.Init(&PropertyValidatorConfig{MaxPropertyPayload: 20})
	cl := newTestClient("client-6")
	pk := packets.Packet{
		FixedHeader: packets.FixedHeader{Type: packets.Publish},
		Properties: packets.Properties{
			User: []packets.UserProperty{
				{Key: "aaaaaaaaaa", Val: "bbbbbbbbbb"}, // 10+10 = 20, at limit
				{Key: "c", Val: "d"},                    // 1+1 = 2, pushes over
			},
		},
	}
	_, err := h.OnPublish(cl, pk)
	if err != packets.ErrRejectPacket {
		t.Fatalf("expected ErrRejectPacket for packet budget exceeded, got %v", err)
	}
}

func TestPropertyValidatorHook_RejectClientBudgetExceeded(t *testing.T) {
	h := &PropertyValidatorHook{}
	_ = h.Init(&PropertyValidatorConfig{MaxClientBudget: 30})
	cl := newTestClient("client-7")

	// First message: 10 bytes total
	pk1 := packets.Packet{
		FixedHeader: packets.FixedHeader{Type: packets.Publish},
		Properties: packets.Properties{
			User: []packets.UserProperty{
				{Key: "abcde", Val: "fghij"}, // 5+5 = 10
			},
		},
	}
	_, err := h.OnPublish(cl, pk1)
	if err != nil {
		t.Fatalf("first message should be allowed (10/30 budget): %v", err)
	}

	// Second message: 10 more bytes → total 20, still ok
	_, err = h.OnPublish(cl, pk1)
	if err != nil {
		t.Fatalf("second message should be allowed (20/30 budget): %v", err)
	}

	// Third message: 10 more → total 30, still ok
	_, err = h.OnPublish(cl, pk1)
	if err != nil {
		t.Fatalf("third message should be allowed (30/30 budget): %v", err)
	}

	// Fourth message: 10 more → total 40, exceeds 30 budget
	_, err = h.OnPublish(cl, pk1)
	if err != packets.ErrRejectPacket {
		t.Fatalf("fourth message should be rejected (40 > 30 budget), got %v", err)
	}
}

func TestPropertyValidatorHook_BudgetResetOnDisconnect(t *testing.T) {
	h := &PropertyValidatorHook{}
	_ = h.Init(&PropertyValidatorConfig{MaxClientBudget: 20})
	cl := newTestClient("client-8")

	pk := packets.Packet{
		FixedHeader: packets.FixedHeader{Type: packets.Publish},
		Properties: packets.Properties{
			User: []packets.UserProperty{
				{Key: "abcde", Val: "fghij"}, // 10 bytes
			},
		},
	}

	// Use 10 of 20 budget
	_, err := h.OnPublish(cl, pk)
	if err != nil {
		t.Fatalf("first message should pass: %v", err)
	}

	// Disconnect resets budget
	h.OnDisconnect(cl, nil, false)

	// Use 10 again — should pass because budget was reset
	_, err = h.OnPublish(cl, pk)
	if err != nil {
		t.Fatalf("message after reconnect should pass: %v", err)
	}
}

func TestPropertyValidatorHook_AllowAtExactLimits(t *testing.T) {
	h := &PropertyValidatorHook{}
	_ = h.Init(&PropertyValidatorConfig{
		MaxProperties:      2,
		MaxKeySize:         5,
		MaxValueSize:       5,
		MaxPropertyPayload: 20,
		MaxClientBudget:    20,
	})
	cl := newTestClient("client-9")
	pk := packets.Packet{
		FixedHeader: packets.FixedHeader{Type: packets.Publish},
		Properties: packets.Properties{
			User: []packets.UserProperty{
				{Key: "abcde", Val: "fghij"}, // 5+5 = 10
				{Key: "klmno", Val: "pqrst"}, // 5+5 = 10
			},
		},
	}
	_, err := h.OnPublish(cl, pk)
	if err != nil {
		t.Fatalf("message exactly at limits should be allowed: %v", err)
	}
}

func TestPropertyValidatorHook_IsolatedClientBudgets(t *testing.T) {
	h := &PropertyValidatorHook{}
	_ = h.Init(&PropertyValidatorConfig{MaxClientBudget: 15})

	cl1 := newTestClient("client-A")
	cl2 := newTestClient("client-B")

	pk := packets.Packet{
		FixedHeader: packets.FixedHeader{Type: packets.Publish},
		Properties: packets.Properties{
			User: []packets.UserProperty{
				{Key: "abcde", Val: "fghij"}, // 10 bytes
			},
		},
	}

	// client-A uses 10/15
	_, err := h.OnPublish(cl1, pk)
	if err != nil {
		t.Fatalf("client-A first message should pass: %v", err)
	}

	// client-B should have its own budget, also 10/15
	_, err = h.OnPublish(cl2, pk)
	if err != nil {
		t.Fatalf("client-B first message should pass (separate budget): %v", err)
	}

	// client-A second message: 20 > 15, should be rejected
	_, err = h.OnPublish(cl1, pk)
	if err != packets.ErrRejectPacket {
		t.Fatalf("client-A should be rejected (20 > 15 budget), got %v", err)
	}

	// client-B second message: 20 > 15, should also be rejected
	_, err = h.OnPublish(cl2, pk)
	if err != packets.ErrRejectPacket {
		t.Fatalf("client-B should be rejected (20 > 15 budget), got %v", err)
	}
}
