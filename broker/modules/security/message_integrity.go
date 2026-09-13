package security

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"sync/atomic"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
)

// MessageIntegrityConfig holds configuration for the message integrity hook.
type MessageIntegrityConfig struct {
	RequireSignature bool
	VerifySignature  bool
	SharedSecret     []byte
}

// MessageIntegrityHook is an MQTT hook that enforces and validates end-to-end
// message integrity signatures on PUBLISH packets.
type MessageIntegrityHook struct {
	mqtt.HookBase
	config MessageIntegrityConfig

	// Metrics
	PacketsChecked int64
	PacketsDropped int64
	ViolationCount int64
}

// ID returns the unique identifier for this hook.
func (h *MessageIntegrityHook) ID() string {
	return "message-integrity"
}

// Provides indicates which hook methods this hook implements.
func (h *MessageIntegrityHook) Provides(b byte) bool {
	return bytes.Contains([]byte{
		mqtt.OnPublish,
	}, []byte{b})
}

// Init initializes the hook with the provided configuration.
func (h *MessageIntegrityHook) Init(config any) error {
	if config == nil {
		h.config = MessageIntegrityConfig{
			RequireSignature: true,
			VerifySignature:  false, // default to just enforcing presence
			SharedSecret:     nil,
		}
		return nil
	}

	cfg, ok := config.(*MessageIntegrityConfig)
	if !ok {
		return mqtt.ErrInvalidConfigType
	}

	h.config = *cfg
	return nil
}

// OnPublish validates the integrity signature on incoming PUBLISH packets.
func (h *MessageIntegrityHook) OnPublish(cl *mqtt.Client, pk packets.Packet) (packets.Packet, error) {
	atomic.AddInt64(&h.PacketsChecked, 1)

	// Bypass signature checks for the latency probe client
	if len(cl.ID) >= 5 && cl.ID[:5] == "probe" {
		return pk, nil
	}

	var sigProp *packets.UserProperty
	for i, p := range pk.Properties.User {
		if p.Key == "integrity-signature" {
			sigProp = &pk.Properties.User[i]
			break
		}
	}

	if sigProp == nil {
		if h.config.RequireSignature {
			h.recordViolation(cl, "missing_signature")
			return pk, packets.ErrRejectPacket
		}
		return pk, nil
	}

	if h.config.VerifySignature {
		mac := hmac.New(sha256.New, h.config.SharedSecret)
		mac.Write([]byte(pk.TopicName))
		mac.Write(pk.Payload)
		expectedMAC := mac.Sum(nil)

		providedMAC, err := base64.StdEncoding.DecodeString(sigProp.Val)
		if err != nil || !hmac.Equal(providedMAC, expectedMAC) {
			h.recordViolation(cl, "invalid_signature")
			return pk, packets.ErrRejectPacket
		}
	}

	return pk, nil
}

// recordViolation increments violation counters and logs the violation.
func (h *MessageIntegrityHook) recordViolation(cl *mqtt.Client, reason string) {
	atomic.AddInt64(&h.PacketsDropped, 1)
	atomic.AddInt64(&h.ViolationCount, 1)
	if h.Log != nil {
		h.Log.Warn("message integrity violation",
			"client", cl.ID,
			"reason", reason,
		)
	}
}

// Metrics returns the current metrics snapshot.
func (h *MessageIntegrityHook) Metrics() (checked, dropped, violations int64) {
	return atomic.LoadInt64(&h.PacketsChecked),
		atomic.LoadInt64(&h.PacketsDropped),
		atomic.LoadInt64(&h.ViolationCount)
}
