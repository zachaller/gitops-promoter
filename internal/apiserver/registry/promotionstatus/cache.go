/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package promotionstatus

import (
	"sync"
	"time"

	"github.com/argoproj-labs/gitops-promoter/internal/apiserver/apis/aggregated/v1alpha1"
)

// CacheEntry represents a cached PromotionStatus with expiration.
type CacheEntry struct {
	Status    *v1alpha1.PromotionStatus
	ExpiresAt time.Time
}

// Cache provides a thread-safe cache for PromotionStatus objects.
type Cache struct {
	entries map[string]*CacheEntry
	ttl     time.Duration
	mu      sync.RWMutex
}

// NewCache creates a new Cache with the specified TTL.
func NewCache(ttl time.Duration) *Cache {
	c := &Cache{
		entries: make(map[string]*CacheEntry),
		ttl:     ttl,
	}
	// Start background cleanup goroutine
	go c.cleanupLoop()
	return c
}

// cacheKey generates a cache key from namespace and name.
func cacheKey(namespace, name string) string {
	return namespace + "/" + name
}

// Get retrieves a PromotionStatus from the cache.
// Returns nil if not found or expired.
func (c *Cache) Get(namespace, name string) *v1alpha1.PromotionStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()

	key := cacheKey(namespace, name)
	entry, ok := c.entries[key]
	if !ok {
		return nil
	}

	if time.Now().After(entry.ExpiresAt) {
		return nil
	}

	return entry.Status.DeepCopy()
}

// Set stores a PromotionStatus in the cache.
func (c *Cache) Set(status *v1alpha1.PromotionStatus) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := cacheKey(status.Namespace, status.Name)
	c.entries[key] = &CacheEntry{
		Status:    status.DeepCopy(),
		ExpiresAt: time.Now().Add(c.ttl),
	}
}

// Delete removes a PromotionStatus from the cache.
func (c *Cache) Delete(namespace, name string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := cacheKey(namespace, name)
	delete(c.entries, key)
}

// Invalidate clears all entries from the cache.
func (c *Cache) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries = make(map[string]*CacheEntry)
}

// cleanupLoop periodically removes expired entries.
func (c *Cache) cleanupLoop() {
	ticker := time.NewTicker(c.ttl)
	defer ticker.Stop()

	for range ticker.C {
		c.cleanup()
	}
}

// cleanup removes expired entries from the cache.
func (c *Cache) cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	for key, entry := range c.entries {
		if now.After(entry.ExpiresAt) {
			delete(c.entries, key)
		}
	}
}

// Size returns the number of entries in the cache.
func (c *Cache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}
