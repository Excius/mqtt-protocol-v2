package security

import (
	"encoding/json"
	"strings"
	"testing"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
)

func TestWildcardTokenHook_Init(t *testing.T) {
	h := new(WildcardTokenHook)

	err := h.Init(nil)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if string(h.config.SharedSecret) != "default-wildcard-secret" {
		t.Errorf("expected default shared secret")
	}

	cfg := &WildcardTokensConfig{
		SharedSecret: []byte("my-secret"),
		Permissions:  map[string][]string{"client1": {"topic/+"}},
	}

	err = h.Init(cfg)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if string(h.config.SharedSecret) != "my-secret" {
		t.Errorf("expected configured shared secret")
	}

	err = h.Init("invalid")
	if err != mqtt.ErrInvalidConfigType {
		t.Errorf("expected ErrInvalidConfigType, got %v", err)
	}
}

func TestWildcardTokenHook_TokenLifecycle(t *testing.T) {
	cfg := &WildcardTokensConfig{
		SharedSecret: []byte("my-secret"),
		Permissions:  map[string][]string{"client1": {"topic/+", "other/#"}},
	}
	h := new(WildcardTokenHook)
	_ = h.Init(cfg)

	cl := &mqtt.Client{ID: "client1"}
	cl2 := &mqtt.Client{ID: "client2"} // no permissions

	// Test OnPacketEncode for Connack
	pkConnack := packets.Packet{
		FixedHeader: packets.FixedHeader{Type: packets.Connack},
		ReasonCode:  packets.CodeSuccess.Code,
	}

	pkOut := h.OnPacketEncode(cl, pkConnack)
	var token string
	for _, p := range pkOut.Properties.User {
		if p.Key == "wildcard-token" {
			token = p.Val
		}
	}
	if token == "" {
		t.Fatalf("expected wildcard-token in CONNACK properties")
	}

	pkOut2 := h.OnPacketEncode(cl2, pkConnack)
	for _, p := range pkOut2.Properties.User {
		if p.Key == "wildcard-token" {
			t.Fatalf("expected no wildcard-token for client2")
		}
	}

	// Test verifyToken manually
	allowed, err := h.verifyToken(token)
	if err != nil {
		t.Fatalf("expected no error verifying token, got %v", err)
	}
	if len(allowed) != 2 || allowed[0] != "topic/+" {
		t.Errorf("unexpected allowed topics in token: %v", allowed)
	}

	// Test OnSubscribe sets the token
	pkSub := packets.Packet{
		Properties: packets.Properties{
			User: []packets.UserProperty{{Key: "wildcard-token", Val: token}},
		},
	}
	_ = h.OnSubscribe(cl, pkSub)

	h.mu.RLock()
	cached, ok := h.activeTokens[cl.ID]
	h.mu.RUnlock()
	if !ok || len(cached) != 2 {
		t.Fatalf("expected token to be cached during subscribe")
	}

	// Test OnACLCheck
	if !h.OnACLCheck(cl, "exact/topic", false) {
		t.Errorf("expected exact topic to be allowed")
	}
	if !h.OnACLCheck(cl, "topic/+", false) {
		t.Errorf("expected authorized wildcard topic to be allowed")
	}
	if h.OnACLCheck(cl, "unauthorized/+", false) {
		t.Errorf("expected unauthorized wildcard topic to be rejected")
	}
	if !h.OnACLCheck(cl, "unauthorized/+", true) {
		t.Errorf("expected publish (write) to be allowed regardless of wildcard")
	}

	// Test OnACLCheck for client without token
	if h.OnACLCheck(cl2, "topic/+", false) {
		t.Errorf("expected client without token to be rejected for wildcard")
	}

	// Test cleanup
	h.OnSubscribed(cl, packets.Packet{}, nil)
	h.mu.RLock()
	_, ok = h.activeTokens[cl.ID]
	h.mu.RUnlock()
	if ok {
		t.Errorf("expected activeTokens to be cleared after subscribe")
	}
}

func TestWildcardTokenHook_VerifyTokenFailure(t *testing.T) {
	h := new(WildcardTokenHook)
	_ = h.Init(nil)

	token := h.generateToken([]string{"#"})
	
	_, err := h.verifyToken("invalid")
	if err == nil {
		t.Errorf("expected error for invalid format")
	}

	payload := WildcardTokenPayload{Allowed: []string{"#"}}
	_, _ = json.Marshal(payload)
	tokenBadPayload := "badbase64." + token[strings.Index(token, ".")+1:]
	_, err = h.verifyToken(tokenBadPayload)
	if err == nil {
		t.Errorf("expected error for bad payload base64")
	}

	tokenBadSignature := token[:strings.Index(token, ".")+1] + "badbase64"
	_, err = h.verifyToken(tokenBadSignature)
	if err == nil {
		t.Errorf("expected error for bad signature base64")
	}

	// Tampered payload
	payloadTampered := WildcardTokenPayload{Allowed: []string{"+/tampered"}}
	_, _ = json.Marshal(payloadTampered)
	tokenTampered := "eyJhbGxvd2VkIjpbIisvdGFtcGVyZWQiXX0=." + token[strings.Index(token, ".")+1:]
	_, err = h.verifyToken(tokenTampered)
	if err == nil {
		t.Errorf("expected error for signature mismatch on tampered payload")
	}
}
