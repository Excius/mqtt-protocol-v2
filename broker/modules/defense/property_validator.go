package defense

import (
	"bytes"
	"sync"
	"sync/atomic"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
)

// Default limits for user property validation.
const (
	DefaultMaxProperties      = 10
	DefaultMaxKeySize         = 256
	DefaultMaxValueSize       = 256
	DefaultMaxPropertyPayload = 4096
	DefaultMaxClientBudget    = 32 * 1024 // 32 KB
)

// PropertyValidatorConfig holds configuration for the property validator hook.
type PropertyValidatorConfig struct {
	MaxProperties      int // maximum number of user properties per message
	MaxKeySize         int // maximum byte length of a property key
	MaxValueSize       int // maximum byte length of a property value
	MaxPropertyPayload int // maximum total byte size of all user properties in a single packet
	MaxClientBudget    int // cumulative per-client user property budget in bytes
}

// PropertyValidatorHook is an MQTT hook that validates user properties on
// PUBLISH packets to prevent metadata-based resource exhaustion attacks.
type PropertyValidatorHook struct {
	mqtt.HookBase
	config        PropertyValidatorConfig
	clientBudgets sync.Map // map[string]*int64 — cumulative bytes per client ID

	// Metrics (atomic counters)
	PacketsChecked int64
	PacketsDropped int64
	ViolationCount int64
}

// ID returns the unique identifier for this hook.
func (h *PropertyValidatorHook) ID() string {
	return "user-property-validator"
}

// Provides indicates which hook methods this hook implements.
func (h *PropertyValidatorHook) Provides(b byte) bool {
	return bytes.Contains([]byte{
		mqtt.OnPublish,
		mqtt.OnDisconnect,
	}, []byte{b})
}

// Init initializes the hook with the provided configuration.
func (h *PropertyValidatorHook) Init(config any) error {
	if config == nil {
		h.config = PropertyValidatorConfig{
			MaxProperties:      DefaultMaxProperties,
			MaxKeySize:         DefaultMaxKeySize,
			MaxValueSize:       DefaultMaxValueSize,
			MaxPropertyPayload: DefaultMaxPropertyPayload,
			MaxClientBudget:    DefaultMaxClientBudget,
		}
		return nil
	}

	cfg, ok := config.(*PropertyValidatorConfig)
	if !ok {
		return mqtt.ErrInvalidConfigType
	}

	h.config = *cfg

	if h.config.MaxProperties <= 0 {
		h.config.MaxProperties = DefaultMaxProperties
	}
	if h.config.MaxKeySize <= 0 {
		h.config.MaxKeySize = DefaultMaxKeySize
	}
	if h.config.MaxValueSize <= 0 {
		h.config.MaxValueSize = DefaultMaxValueSize
	}
	if h.config.MaxPropertyPayload <= 0 {
		h.config.MaxPropertyPayload = DefaultMaxPropertyPayload
	}
	if h.config.MaxClientBudget <= 0 {
		h.config.MaxClientBudget = DefaultMaxClientBudget
	}

	return nil
}

// OnPublish validates user properties on incoming PUBLISH packets.
// Returns ErrRejectPacket if any limits are exceeded.
func (h *PropertyValidatorHook) OnPublish(cl *mqtt.Client, pk packets.Packet) (packets.Packet, error) {
	atomic.AddInt64(&h.PacketsChecked, 1)

	props := pk.Properties.User
	if len(props) == 0 {
		return pk, nil
	}

	// Check 1: property count limit
	if len(props) > h.config.MaxProperties {
		h.recordViolation(cl, "property_count_exceeded")
		return pk, packets.ErrRejectPacket
	}

	totalSize := 0
	for _, p := range props {
		keyLen := len(p.Key)
		valLen := len(p.Val)

		// Check 2: individual key size
		if keyLen > h.config.MaxKeySize {
			h.recordViolation(cl, "key_size_exceeded")
			return pk, packets.ErrRejectPacket
		}

		// Check 3: individual value size
		if valLen > h.config.MaxValueSize {
			h.recordViolation(cl, "value_size_exceeded")
			return pk, packets.ErrRejectPacket
		}

		totalSize += keyLen + valLen
	}

	// Check 4: total packet property payload
	if totalSize > h.config.MaxPropertyPayload {
		h.recordViolation(cl, "packet_property_budget_exceeded")
		return pk, packets.ErrRejectPacket
	}

	// Check 5: per-client cumulative budget
	budget := h.getClientBudget(cl.ID)
	newTotal := atomic.AddInt64(budget, int64(totalSize))
	if newTotal > int64(h.config.MaxClientBudget) {
		h.recordViolation(cl, "client_budget_exceeded")
		return pk, packets.ErrRejectPacket
	}

	return pk, nil
}

// OnDisconnect cleans up per-client budget tracking when a client disconnects.
func (h *PropertyValidatorHook) OnDisconnect(cl *mqtt.Client, err error, expire bool) {
	h.clientBudgets.Delete(cl.ID)
}

// recordViolation increments violation counters and logs the violation.
func (h *PropertyValidatorHook) recordViolation(cl *mqtt.Client, reason string) {
	atomic.AddInt64(&h.PacketsDropped, 1)
	atomic.AddInt64(&h.ViolationCount, 1)
	if h.Log != nil {
		h.Log.Warn("user property violation",
			"client", cl.ID,
			"reason", reason,
			"packets_dropped", atomic.LoadInt64(&h.PacketsDropped),
		)
	}
}

// getClientBudget returns the cumulative budget counter for a client.
func (h *PropertyValidatorHook) getClientBudget(clientID string) *int64 {
	val, loaded := h.clientBudgets.LoadOrStore(clientID, new(int64))
	if !loaded {
		return val.(*int64)
	}
	return val.(*int64)
}

// Metrics returns the current metrics snapshot.
func (h *PropertyValidatorHook) Metrics() (checked, dropped, violations int64) {
	return atomic.LoadInt64(&h.PacketsChecked),
		atomic.LoadInt64(&h.PacketsDropped),
		atomic.LoadInt64(&h.ViolationCount)
}
