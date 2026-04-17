package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveEnabledModulesLegacyFlags(t *testing.T) {
	modules, err := resolveEnabledModules(brokerConfig{
		tlsSessionResumption: true,
	})
	require.NoError(t, err)
	require.Equal(t, []string{moduleTLSSessionResumption}, modules)

	modules, err = resolveEnabledModules(brokerConfig{
		tlsSessionResumption: false,
	})
	require.NoError(t, err)
	require.Empty(t, modules)
}

func TestResolveEnabledModulesExplicitModes(t *testing.T) {
	modules, err := resolveEnabledModules(brokerConfig{
		modules: "baseline",
	})
	require.NoError(t, err)
	require.Empty(t, modules)

	modules, err = resolveEnabledModules(brokerConfig{
		modules: "tls-session-resumption",
	})
	require.NoError(t, err)
	require.Equal(t, []string{moduleTLSSessionResumption}, modules)

	_, err = resolveEnabledModules(brokerConfig{
		modules: "baseline,tls-session-resumption",
	})
	require.Error(t, err)

	modules, err = resolveEnabledModules(brokerConfig{
		modules: "adaptive-tls-profiles",
	})
	require.NoError(t, err)
	require.Equal(t, []string{moduleAdaptiveTLSProfiles}, modules)

	modules, err = resolveEnabledModules(brokerConfig{
		modules: "tls-session-resumption,adaptive-tls-profiles",
	})
	require.NoError(t, err)
	require.Equal(t, []string{moduleTLSSessionResumption, moduleAdaptiveTLSProfiles}, modules)

	_, err = resolveEnabledModules(brokerConfig{
		modules: "unknown-module",
	})
	require.Error(t, err)
}

func TestBuildBrokerRuntime(t *testing.T) {
	runtime, err := buildBrokerRuntime([]string{moduleTLSSessionResumption}, brokerConfig{
		tlsProfile: "BALANCED",
	})
	require.NoError(t, err)
	require.True(t, runtime.tlsSessionResumption)
	require.Equal(t, []string{moduleTLSSessionResumption}, runtime.enabledModules)
	require.Equal(t, "BALANCED", runtime.tlsProfile)

	runtime, err = buildBrokerRuntime([]string{moduleAdaptiveTLSProfiles}, brokerConfig{
		tlsProfile: "LOW_POWER",
	})
	require.NoError(t, err)
	require.True(t, runtime.adaptiveTLSProfiles)
	require.Equal(t, "LOW_POWER", runtime.tlsProfile)

	runtime, err = buildBrokerRuntime([]string{moduleTLSSessionResumption, moduleAdaptiveTLSProfiles}, brokerConfig{
		tlsProfile: "HIGH_SECURITY",
	})
	require.NoError(t, err)
	require.True(t, runtime.tlsSessionResumption)
	require.True(t, runtime.adaptiveTLSProfiles)
	require.Equal(t, "HIGH_SECURITY", runtime.tlsProfile)

	runtime, err = buildBrokerRuntime(nil, brokerConfig{
		tlsProfile: "BALANCED",
	})
	require.NoError(t, err)
	require.False(t, runtime.tlsSessionResumption)
	require.Empty(t, runtime.enabledModules)
	require.Equal(t, "BALANCED", runtime.tlsProfile)

	_, err = buildBrokerRuntime(nil, brokerConfig{
		tlsProfile: "LOW_POWER",
	})
	require.Error(t, err)

	_, err = buildBrokerRuntime([]string{moduleAdaptiveTLSProfiles}, brokerConfig{
		tlsProfile: "INVALID",
	})
	require.Error(t, err)
}

func TestDefaultTLSProfileFromEnv(t *testing.T) {
	t.Setenv("TLS_PROFILE", "")
	t.Setenv("PROFILE", "")
	t.Setenv("MQTT_TLS_PROFILE", "")
	require.Equal(t, "BALANCED", defaultTLSProfileFromEnv())

	t.Setenv("MQTT_TLS_PROFILE", "LOW_POWER")
	require.Equal(t, "LOW_POWER", defaultTLSProfileFromEnv())

	t.Setenv("PROFILE", "HIGH_SECURITY")
	require.Equal(t, "HIGH_SECURITY", defaultTLSProfileFromEnv())

	t.Setenv("TLS_PROFILE", "BALANCED")
	require.Equal(t, "BALANCED", defaultTLSProfileFromEnv())
}
