// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package e2elib

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSelectedRateLimitStorage(t *testing.T) {
	tests := []struct {
		name          string
		value         string
		wantName      string
		wantNamespace string
		wantURL       string
		wantCLI       string
	}{
		{
			name:          "default",
			wantName:      "Redis",
			wantNamespace: "redis-system",
			wantURL:       "redis.redis-system.svc.cluster.local:6379",
			wantCLI:       "redis-cli",
		},
		{
			name:          "redis",
			value:         "redis",
			wantName:      "Redis",
			wantNamespace: "redis-system",
			wantURL:       "redis.redis-system.svc.cluster.local:6379",
			wantCLI:       "redis-cli",
		},
		{
			name:          "valkey",
			value:         "valkey",
			wantName:      "Valkey",
			wantNamespace: "valkey-system",
			wantURL:       "valkey.valkey-system.svc.cluster.local:6379",
			wantCLI:       "valkey-cli",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(RateLimitStorageEnvVar, tt.value)
			storage, err := SelectedRateLimitStorage()
			require.NoError(t, err)
			require.Equal(t, tt.wantName, storage.Name)
			require.Equal(t, tt.wantNamespace, storage.Namespace)
			require.Equal(t, tt.wantURL, storage.URL)
			// CLI has to be asserted by value: it is the client the counter
			// readback execs in the pod, so a wrong one breaks every assertion
			// the rate limit e2e tests make.
			require.Equal(t, tt.wantCLI, storage.CLI)
			require.NotEmpty(t, storage.Manifest)
			require.NotEmpty(t, storage.ValuesAddon)
			require.NotEmpty(t, storage.PodSelector)
			require.NotEmpty(t, storage.Deployment)
		})
	}
}

func TestSelectedRateLimitStorageRejectsUnsupportedValue(t *testing.T) {
	t.Setenv(RateLimitStorageEnvVar, "valkeyy")
	_, err := SelectedRateLimitStorage()
	require.ErrorContains(t, err, `unsupported E2E_RATELIMIT_STORAGE value "valkeyy"`)
}
