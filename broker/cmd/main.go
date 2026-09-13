// SPDX-License-Identifier: MIT
// SPDX-FileCopyrightText: 2022 mochi-mqtt, mochi-co
// SPDX-FileContributor: mochi-co

package main

import (
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mochi-mqtt/server/v2/modules/defense"
	"github.com/mochi-mqtt/server/v2/modules/defense/ebpf"
	"github.com/mochi-mqtt/server/v2/modules/routing"
	tlsprofiles "github.com/mochi-mqtt/server/v2/modules/security"
)

const (
	moduleBaseline             = "baseline"
	moduleTLSSessionResumption = "tls-session-resumption"
	moduleAdaptiveTLSProfiles  = "adaptive-tls-profiles"
	modulePropertyValidator    = "property-validator"
	moduleAuthDefense          = "auth-defense"
	moduleMessageIntegrity     = "message-integrity"
	modulePriorityMessaging    = "priority-messaging"
	moduleWildcardTokens       = "wildcard-tokens"
	moduleQUICTransport        = "quic-transport"
	moduleEBPFFilter           = "ebpf-filter"
)

type brokerConfig struct {
	tcpAddr              string
	wsAddr               string
	infoAddr             string
	tlsCertFile          string
	tlsKeyFile           string
	tlsSessionResumption bool
	tlsProfile           string
	modules              string
	quicAddr             string
}

type brokerRuntime struct {
	enabledModules       []string
	tlsSessionResumption bool
	adaptiveTLSProfiles  bool
	propertyValidator    bool
	authDefense          bool
	messageIntegrity     bool
	priorityMessaging    bool
	wildcardTokens       bool
	quicTransport        bool
	ebpfFilter           bool
	tlsProfile           string
}

type brokerModule func(runtime *brokerRuntime, cfg brokerConfig) error

var supportedModules = map[string]struct{}{
	moduleBaseline:             {},
	moduleTLSSessionResumption: {},
	moduleAdaptiveTLSProfiles:  {},
	modulePropertyValidator:    {},
	moduleAuthDefense:          {},
	moduleMessageIntegrity:     {},
	modulePriorityMessaging:    {},
	moduleWildcardTokens:       {},
	moduleQUICTransport:        {},
	moduleEBPFFilter:           {},
}

var moduleRegistry = map[string]brokerModule{
	moduleTLSSessionResumption: func(runtime *brokerRuntime, _ brokerConfig) error {
		runtime.tlsSessionResumption = true
		return nil
	},
	moduleAdaptiveTLSProfiles: func(runtime *brokerRuntime, cfg brokerConfig) error {
		profile, err := tlsprofiles.NormalizeTLSProfile(cfg.tlsProfile)
		if err != nil {
			return err
		}

		runtime.adaptiveTLSProfiles = true
		runtime.tlsProfile = profile
		return nil
	},
	modulePropertyValidator: func(runtime *brokerRuntime, _ brokerConfig) error {
		runtime.propertyValidator = true
		return nil
	},
	moduleAuthDefense: func(runtime *brokerRuntime, _ brokerConfig) error {
		runtime.authDefense = true
		return nil
	},
	moduleMessageIntegrity: func(runtime *brokerRuntime, _ brokerConfig) error {
		runtime.messageIntegrity = true
		return nil
	},
	modulePriorityMessaging: func(runtime *brokerRuntime, _ brokerConfig) error {
		runtime.priorityMessaging = true
		return nil
	},
	moduleWildcardTokens: func(runtime *brokerRuntime, _ brokerConfig) error {
		runtime.wildcardTokens = true
		return nil
	},
	moduleQUICTransport: func(runtime *brokerRuntime, _ brokerConfig) error {
		runtime.quicTransport = true
		return nil
	},
	moduleEBPFFilter: func(runtime *brokerRuntime, _ brokerConfig) error {
		runtime.ebpfFilter = true
		return nil
	},
}

func main() {
	cfg := parseBrokerConfig()

	if err := validateBrokerConfig(cfg); err != nil {
		log.Fatal(err)
	}

	enabledModules, err := resolveEnabledModules(cfg)
	if err != nil {
		log.Fatal(err)
	}

	runtime, err := buildBrokerRuntime(enabledModules, cfg)
	if err != nil {
		log.Fatal(err)
	}

	tlsConfig, err := buildTLSConfig(cfg, runtime)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("broker modules enabled: %s", moduleListForLog(runtime.enabledModules))
	log.Printf("Using TLS Profile: %s", runtime.tlsProfile)

	sigs := make(chan os.Signal, 1)
	done := make(chan bool, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		done <- true
	}()

	server := mqtt.New(nil)
	if err := configureAuthHook(server); err != nil {
		log.Fatal(err)
	}

	if runtime.propertyValidator {
		if err := server.AddHook(new(defense.PropertyValidatorHook), nil); err != nil {
			log.Fatal(fmt.Errorf("failed to add property-validator hook: %w", err))
		}
		log.Println("property-validator module enabled")
	}

	if runtime.authDefense {
		if err := server.AddHook(new(defense.AuthDefenseHook), nil); err != nil {
			log.Fatal(fmt.Errorf("failed to add auth-defense hook: %w", err))
		}
		log.Println("auth-defense module enabled")
	}

	if runtime.messageIntegrity {
		// Secure default configuration for the broker
		cfg := &tlsprofiles.MessageIntegrityConfig{
			RequireSignature: true,
			VerifySignature:  true,
			SharedSecret:     []byte("default-broker-secret"), // In a real deployment, this would be loaded from env/config
		}
		if err := server.AddHook(new(tlsprofiles.MessageIntegrityHook), cfg); err != nil {
			log.Fatal(fmt.Errorf("failed to add message-integrity hook: %w", err))
		}
		log.Println("message-integrity module enabled")
	}

	if runtime.ebpfFilter {
		if err := server.AddHook(new(ebpf.EBPFFilterHook), &ebpf.Options{MaxConnectionsPerSecond: 10}); err != nil {
			log.Fatal(fmt.Errorf("failed to add ebpf-filter hook: %w", err))
		}
		log.Println("ebpf-filter module enabled")
	}

	if runtime.priorityMessaging {
		if err := server.AddHook(new(routing.PriorityMessagingHook), nil); err != nil {
			log.Fatal(fmt.Errorf("failed to add priority-messaging hook: %w", err))
		}
		log.Println("priority-messaging module enabled")
	}

	if runtime.wildcardTokens {
		// Use a simple configuration mapping for demonstration
		cfg := &tlsprofiles.WildcardTokensConfig{
			SharedSecret: []byte("wildcard-broker-secret"),
			Permissions: map[string][]string{
				"admin": {"#"},
				"sensor-1": {"sensors/1/+"},
				"load-*": {"test/load"},
				"probe-*": {"test/latency"},
			},
		}
		if err := server.AddHook(new(tlsprofiles.WildcardTokenHook), cfg); err != nil {
			log.Fatal(fmt.Errorf("failed to add wildcard-tokens hook: %w", err))
		}
		log.Println("wildcard-tokens module enabled")
	}

	tcp := listeners.NewTCP(listeners.Config{
		ID:        "t1",
		Address:   cfg.tcpAddr,
		TLSConfig: tlsConfig,
	})
	err = server.AddListener(tcp)
	if err != nil {
		log.Fatal(err)
	}

	if runtime.quicTransport {
		if tlsConfig == nil {
			log.Fatal("quic-transport requires TLS to be configured (--tls-cert-file and --tls-key-file)")
		}
		quicListener := listeners.NewQUIC(listeners.Config{
			ID:        "q1",
			Address:   cfg.quicAddr,
			TLSConfig: tlsConfig,
		})
		err = server.AddListener(quicListener)
		if err != nil {
			log.Fatal(err)
		}
		log.Println("quic-transport module enabled")
	}

	ws := listeners.NewWebsocket(listeners.Config{
		ID:      "ws1",
		Address: cfg.wsAddr,
	})
	err = server.AddListener(ws)
	if err != nil {
		log.Fatal(err)
	}

	stats := listeners.NewHTTPStats(
		listeners.Config{
			ID:      "info",
			Address: cfg.infoAddr,
		},
		server.Info,
	)
	err = server.AddListener(stats)
	if err != nil {
		log.Fatal(err)
	}

	go func() {
		err := server.Serve()
		if err != nil {
			log.Fatal(err)
		}
	}()

	<-done
	server.Log.Warn("caught signal, stopping...")
	_ = server.Close()
	server.Log.Info("mochi mqtt shutdown complete")
}

func parseBrokerConfig() brokerConfig {
	tcpAddr := flag.String("tcp", ":1883", "network address for TCP listener")
	wsAddr := flag.String("ws", ":1882", "network address for Websocket listener")
	infoAddr := flag.String("info", ":8080", "network address for web info dashboard listener")
	tlsCertFile := flag.String("tls-cert-file", "", "TLS certificate file")
	tlsKeyFile := flag.String("tls-key-file", "", "TLS key file")
	tlsSessionResumption := flag.Bool("tls-session-resumption", true, "enable TLS session resumption tickets (legacy toggle)")
	tlsProfile := flag.String("tls-profile", defaultTLSProfileFromEnv(), "TLS profile (LOW_POWER|BALANCED|HIGH_SECURITY). Can be set by TLS_PROFILE, PROFILE, or MQTT_TLS_PROFILE.")
	quicAddr := flag.String("quic", ":1883", "network address for QUIC listener")
	modules := flag.String("modules", "", "comma-separated modules: baseline,tls-session-resumption,adaptive-tls-profiles,property-validator,auth-defense,message-integrity,priority-messaging,wildcard-tokens,quic-transport,ebpf-filter")
	flag.Parse()

	return brokerConfig{
		tcpAddr:              *tcpAddr,
		wsAddr:               *wsAddr,
		infoAddr:             *infoAddr,
		tlsCertFile:          *tlsCertFile,
		tlsKeyFile:           *tlsKeyFile,
		tlsSessionResumption: *tlsSessionResumption,
		tlsProfile:           *tlsProfile,
		modules:              *modules,
		quicAddr:             *quicAddr,
	}
}

func validateBrokerConfig(cfg brokerConfig) error {
	if (cfg.tlsCertFile == "") != (cfg.tlsKeyFile == "") {
		return errors.New("both --tls-cert-file and --tls-key-file are required for TLS mode")
	}

	if _, err := tlsprofiles.NormalizeTLSProfile(cfg.tlsProfile); err != nil {
		return err
	}

	return nil
}

func resolveEnabledModules(cfg brokerConfig) ([]string, error) {
	modulesFlag := strings.TrimSpace(cfg.modules)
	if modulesFlag == "" {
		if cfg.tlsSessionResumption {
			return []string{moduleTLSSessionResumption}, nil
		}
		return []string{}, nil
	}

	rawModules := strings.Split(modulesFlag, ",")
	enabled := make([]string, 0, len(rawModules))
	seen := make(map[string]struct{}, len(rawModules))
	baselineRequested := false

	for _, raw := range rawModules {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			continue
		}

		if _, ok := supportedModules[name]; !ok {
			return nil, fmt.Errorf("unknown module %q (supported: baseline, tls-session-resumption, adaptive-tls-profiles, property-validator, auth-defense, message-integrity, priority-messaging, wildcard-tokens, quic-transport, ebpf-filter)", name)
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}

		if name == moduleBaseline {
			baselineRequested = true
			continue
		}
		enabled = append(enabled, name)
	}

	if baselineRequested && len(enabled) > 0 {
		return nil, errors.New("module baseline cannot be combined with other modules")
	}
	if baselineRequested {
		return []string{}, nil
	}
	if len(enabled) == 0 {
		return nil, errors.New("--modules provided but no valid module names found")
	}

	return enabled, nil
}

func buildBrokerRuntime(enabledModules []string, cfg brokerConfig) (brokerRuntime, error) {
	runtime := brokerRuntime{
		enabledModules: append([]string(nil), enabledModules...),
		tlsProfile:     tlsprofiles.ProfileBalanced,
	}

	for _, moduleName := range enabledModules {
		applyModule, ok := moduleRegistry[moduleName]
		if !ok {
			return brokerRuntime{}, fmt.Errorf("unsupported module %q", moduleName)
		}
		if err := applyModule(&runtime, cfg); err != nil {
			return brokerRuntime{}, fmt.Errorf("%s: %w", moduleName, err)
		}
	}

	if !runtime.adaptiveTLSProfiles {
		selectedProfile, err := tlsprofiles.NormalizeTLSProfile(cfg.tlsProfile)
		if err != nil {
			return brokerRuntime{}, err
		}
		if selectedProfile != tlsprofiles.ProfileBalanced {
			return brokerRuntime{}, errors.New("non-balanced TLS profile requires adaptive-tls-profiles module")
		}
	}

	return runtime, nil
}

func buildTLSConfig(cfg brokerConfig, runtime brokerRuntime) (*tls.Config, error) {
	if cfg.tlsCertFile == "" || cfg.tlsKeyFile == "" {
		return nil, nil
	}

	cert, err := tls.LoadX509KeyPair(cfg.tlsCertFile, cfg.tlsKeyFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load TLS certificate/key pair: %w", err)
	}

	tlsProfile := tlsprofiles.ProfileBalanced
	if runtime.adaptiveTLSProfiles {
		tlsProfile = runtime.tlsProfile
	}

	tlsConfig := tlsprofiles.GetTLSConfig(tlsProfile)
	tlsConfig.SessionTicketsDisabled = !runtime.tlsSessionResumption
	tlsConfig.Certificates = []tls.Certificate{cert}
	// QUIC mandates a successful ALPN negotiation as part of its TLS 1.3
	// handshake (unlike plain TLS-over-TCP, where ALPN is optional and
	// simply goes unused by non-ALPN-aware clients). This same *tls.Config
	// is shared by both the TCP and QUIC listeners below, so set it
	// unconditionally: without it, every QUIC client fails to connect with
	// "tls: server did not select an ALPN protocol", regardless of the
	// active TLS profile.
	tlsConfig.NextProtos = []string{"mqtt"}
	return tlsConfig, nil
}

func configureAuthHook(server *mqtt.Server) error {
	if err := server.AddHook(new(auth.AllowHook), nil); err != nil {
		return fmt.Errorf("failed to add allow-all auth hook: %w", err)
	}
	return nil
}

func moduleListForLog(enabledModules []string) string {
	if len(enabledModules) == 0 {
		return moduleBaseline
	}
	return strings.Join(enabledModules, ",")
}

func defaultTLSProfileFromEnv() string {
	if profile := strings.TrimSpace(os.Getenv("TLS_PROFILE")); profile != "" {
		return profile
	}
	if profile := strings.TrimSpace(os.Getenv("PROFILE")); profile != "" {
		return profile
	}
	if profile := strings.TrimSpace(os.Getenv("MQTT_TLS_PROFILE")); profile != "" {
		return profile
	}
	return tlsprofiles.ProfileBalanced
}
