// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package e2e

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/tests/internal/e2elib"
)

// defaultRateLimitStorageURL is the Redis URL the checked-in rate limit manifests
// use. applyQuotaRateLimitManifest rewrites it when the tests run against Valkey.
const defaultRateLimitStorageURL = "redis.redis-system.svc.cluster.local:6379"

// applyRateLimitStorage deploys the Redis-protocol backend selected by
// E2E_RATELIMIT_STORAGE and waits for it to be ready.
func applyRateLimitStorage(t *testing.T) *e2elib.RateLimitStorage {
	t.Helper()
	storage, err := e2elib.SelectedRateLimitStorage()
	require.NoError(t, err)
	require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), storage.Manifest))
	t.Cleanup(func() {
		_ = e2elib.KubectlDeleteManifest(context.Background(), storage.Manifest)
	})
	e2elib.RequireWaitForPodReady(t, storage.Namespace, storage.PodSelector)
	return storage
}

// applyQuotaRateLimitManifest applies the quota rate limit manifest with its
// backend URL pointed at the selected storage. The manifest is checked in with
// the Redis URL, so running against Valkey means rendering it through stdin
// rather than applying the file as-is.
func applyQuotaRateLimitManifest(t *testing.T, storage *e2elib.RateLimitStorage) {
	t.Helper()
	const manifest = "testdata/backend_quota_ratelimit.yaml"
	if storage.URL == defaultRateLimitStorageURL {
		require.NoError(t, e2elib.KubectlApplyManifest(t.Context(), manifest))
		t.Cleanup(func() {
			_ = e2elib.KubectlDeleteManifest(context.Background(), manifest)
		})
		return
	}

	contents, err := os.ReadFile(manifest)
	require.NoError(t, err)
	rendered := strings.ReplaceAll(string(contents), defaultRateLimitStorageURL, storage.URL)
	require.NotEqual(t, string(contents), rendered,
		"%s no longer references %q, so the backend URL cannot be pointed at %s",
		manifest, defaultRateLimitStorageURL, storage.Name)

	require.NoError(t, e2elib.KubectlApplyManifestStdin(t.Context(), rendered))
	t.Cleanup(func() {
		_ = e2elib.KubectlDeleteManifestStdin(context.Background(), rendered)
	})
}
