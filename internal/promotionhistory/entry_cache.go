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

package promotionhistory

import (
	"sync"

	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
)

// EntryCache stores immutable history entries keyed by merged-target SHA.
type EntryCache struct {
	mu      sync.Mutex
	maxSize int
	order   []string
	entries map[string]promoterv1alpha1.History
}

// NewEntryCache creates a bounded LRU cache for history entries.
func NewEntryCache(maxSize int) *EntryCache {
	if maxSize < 1 {
		maxSize = 1
	}
	return &EntryCache{
		maxSize: maxSize,
		entries: make(map[string]promoterv1alpha1.History),
	}
}

// Get returns a cached entry for sha, if present.
func (c *EntryCache) Get(sha string) (promoterv1alpha1.History, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h, ok := c.entries[sha]
	return h, ok
}

// Put stores an entry for sha.
func (c *EntryCache) Put(sha string, entry promoterv1alpha1.History) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[sha]; !exists {
		c.order = append(c.order, sha)
	}
	c.entries[sha] = entry
	for len(c.order) > c.maxSize {
		evict := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, evict)
	}
}

// EnvState tracks the last computed rev-list for one CTP.
type EnvState struct {
	LastTipSha string
	LastSHAs   []string
	LastResult []promoterv1alpha1.History
}

// EnvStateStore holds per-CTP environment history state.
type EnvStateStore struct {
	mu    sync.Mutex
	state map[string]*EnvState
}

// NewEnvStateStore creates an empty env state store.
func NewEnvStateStore() *EnvStateStore {
	return &EnvStateStore{state: make(map[string]*EnvState)}
}

func (s *EnvStateStore) Get(ctpKey string) *EnvState {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.state[ctpKey]
	if !ok {
		st = &EnvState{}
		s.state[ctpKey] = st
	}
	return st
}

func (s *EnvStateStore) Set(ctpKey string, st *EnvState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state[ctpKey] = st
}
