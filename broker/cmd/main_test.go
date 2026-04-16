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

	_, err = resolveEnabledModules(brokerConfig{
		modules: "unknown-module",
	})
	require.Error(t, err)
}

func TestBuildBrokerRuntime(t *testing.T) {
	runtime, err := buildBrokerRuntime([]string{moduleTLSSessionResumption})
	require.NoError(t, err)
	require.True(t, runtime.tlsSessionResumption)
	require.Equal(t, []string{moduleTLSSessionResumption}, runtime.enabledModules)

	runtime, err = buildBrokerRuntime(nil)
	require.NoError(t, err)
	require.False(t, runtime.tlsSessionResumption)
	require.Empty(t, runtime.enabledModules)
}
