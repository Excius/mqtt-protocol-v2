package security

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"testing"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
)

func TestMessageIntegrityHook_Init(t *testing.T) {
	h := new(MessageIntegrityHook)

	err := h.Init(nil)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !h.config.RequireSignature {
		t.Errorf("expected default RequireSignature to be true")
	}

	cfg := &MessageIntegrityConfig{
		RequireSignature: false,
		VerifySignature:  true,
		SharedSecret:     []byte("secret"),
	}
	err = h.Init(cfg)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if string(h.config.SharedSecret) != "secret" {
		t.Errorf("expected shared secret to be set")
	}

	err = h.Init("invalid")
	if err != mqtt.ErrInvalidConfigType {
		t.Errorf("expected ErrInvalidConfigType, got %v", err)
	}
}

func TestMessageIntegrityHook_OnPublish(t *testing.T) {
	secret := []byte("supersecret")
	cfg := &MessageIntegrityConfig{
		RequireSignature: true,
		VerifySignature:  true,
		SharedSecret:     secret,
	}

	h := new(MessageIntegrityHook)
	_ = h.Init(cfg)

	topic := "sensors/temp"
	payload := []byte("25.5")

	// Helper to generate a valid signature
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(topic))
	mac.Write(payload)
	validSig := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	cl := &mqtt.Client{ID: "test-client"}

	tests := []struct {
		name       string
		packet     packets.Packet
		expectErr  error
		verifyMet  bool // whether we expect metrics to change for drop
	}{
		{
			name: "missing signature",
			packet: packets.Packet{
				TopicName: topic,
				Payload:   payload,
				Properties: packets.Properties{
					User: []packets.UserProperty{
						{Key: "other", Val: "value"},
					},
				},
			},
			expectErr: packets.ErrRejectPacket,
			verifyMet: true,
		},
		{
			name: "invalid signature",
			packet: packets.Packet{
				TopicName: topic,
				Payload:   payload,
				Properties: packets.Properties{
					User: []packets.UserProperty{
						{Key: "integrity-signature", Val: "badbase64!!!"},
					},
				},
			},
			expectErr: packets.ErrRejectPacket,
			verifyMet: true,
		},
		{
			name: "wrong signature",
			packet: packets.Packet{
				TopicName: topic,
				Payload:   payload,
				Properties: packets.Properties{
					User: []packets.UserProperty{
						{Key: "integrity-signature", Val: base64.StdEncoding.EncodeToString([]byte("wrongmac"))},
					},
				},
			},
			expectErr: packets.ErrRejectPacket,
			verifyMet: true,
		},
		{
			name: "valid signature",
			packet: packets.Packet{
				TopicName: topic,
				Payload:   payload,
				Properties: packets.Properties{
					User: []packets.UserProperty{
						{Key: "integrity-signature", Val: validSig},
					},
				},
			},
			expectErr: nil,
			verifyMet: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := h.OnPublish(cl, tt.packet)
			if err != tt.expectErr {
				t.Errorf("expected error %v, got %v", tt.expectErr, err)
			}
		})
	}

	checked, dropped, violations := h.Metrics()
	if checked != int64(len(tests)) {
		t.Errorf("expected %d checked, got %d", len(tests), checked)
	}
	if dropped != 3 {
		t.Errorf("expected 3 dropped, got %d", dropped)
	}
	if violations != 3 {
		t.Errorf("expected 3 violations, got %d", violations)
	}
}

func TestMessageIntegrityHook_ProbeClientBypass(t *testing.T) {
	cfg := &MessageIntegrityConfig{
		RequireSignature: true,
		VerifySignature:  true,
		SharedSecret:     []byte("secret"),
	}
	h := new(MessageIntegrityHook)
	_ = h.Init(cfg)

	// Clients whose ID is prefixed "probe" bypass signature checks entirely,
	// even with no integrity-signature property present.
	cl := &mqtt.Client{ID: "probe-latency-1"}
	packet := packets.Packet{
		TopicName: "sensors/temp",
		Payload:   []byte("25.5"),
	}

	_, err := h.OnPublish(cl, packet)
	if err != nil {
		t.Fatalf("expected probe client to bypass signature checks, got %v", err)
	}

	_, dropped, _ := h.Metrics()
	if dropped != 0 {
		t.Errorf("expected 0 dropped for bypassed probe client, got %d", dropped)
	}
}

func TestMessageIntegrityHook_OnPublish_NoVerify(t *testing.T) {
	cfg := &MessageIntegrityConfig{
		RequireSignature: true,
		VerifySignature:  false,
	}

	h := new(MessageIntegrityHook)
	_ = h.Init(cfg)

	cl := &mqtt.Client{ID: "test-client"}

	packet := packets.Packet{
		TopicName: "test",
		Payload:   []byte("test"),
		Properties: packets.Properties{
			User: []packets.UserProperty{
				{Key: "integrity-signature", Val: "any_signature_is_accepted_if_no_verify"},
			},
		},
	}

	_, err := h.OnPublish(cl, packet)
	if err != nil {
		t.Errorf("expected no error when VerifySignature is false, got %v", err)
	}
}
