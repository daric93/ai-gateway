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
	}{
		{
			name:          "default",
			wantName:      "Redis",
			wantNamespace: "redis-system",
			wantURL:       "redis.redis-system.svc.cluster.local:6379",
		},
		{
			name:          "redis",
			value:         "redis",
			wantName:      "Redis",
			wantNamespace: "redis-system",
			wantURL:       "redis.redis-system.svc.cluster.local:6379",
		},
		{
			name:          "valkey",
			value:         "valkey",
			wantName:      "Valkey",
			wantNamespace: "valkey-system",
			wantURL:       "valkey.valkey-system.svc.cluster.local:6379",
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
			require.NotEmpty(t, storage.Manifest)
			require.NotEmpty(t, storage.ValuesAddon)
			require.NotEmpty(t, storage.PodSelector)
			require.NotEmpty(t, storage.Deployment)
			require.NotEmpty(t, storage.CLI)
		})
	}
}

func TestSelectedRateLimitStorageRejectsUnsupportedValue(t *testing.T) {
	t.Setenv(RateLimitStorageEnvVar, "valkeyy")
	_, err := SelectedRateLimitStorage()
	require.ErrorContains(t, err, `unsupported E2E_RATELIMIT_STORAGE value "valkeyy"`)
}
