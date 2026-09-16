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
	"context"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

type calculateTimings struct {
	revList            time.Duration
	prefetchActive     time.Duration
	prefetchNotes      time.Duration
	proposedCollect    time.Duration
	proposedFetch      time.Duration
	proposedLoad       time.Duration
	proposedCount      int
	proposedCached     int
	proposedFetchError string
	buildEntries       time.Duration
}

func (t calculateTimings) log(ctx context.Context) {
	log.FromContext(ctx).Info("promotion history calculate phases",
		"revList", t.revList.Round(time.Millisecond).String(),
		"prefetchActive", t.prefetchActive.Round(time.Millisecond).String(),
		"prefetchNotes", t.prefetchNotes.Round(time.Millisecond).String(),
		"proposedCollect", t.proposedCollect.Round(time.Millisecond).String(),
		"proposedFetch", t.proposedFetch.Round(time.Millisecond).String(),
		"proposedLoad", t.proposedLoad.Round(time.Millisecond).String(),
		"proposedCount", t.proposedCount,
		"proposedCached", t.proposedCached,
		"proposedFetchError", t.proposedFetchError,
		"buildEntries", t.buildEntries.Round(time.Millisecond).String(),
	)
}
