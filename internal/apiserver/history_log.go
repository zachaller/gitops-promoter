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

package apiserver

import (
	"time"

	viewv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/view/v1alpha1"
)

// historyLog is the logger for PromotionStrategyHistory rebuild timing.
var historyLog = log.WithName("history")

func countHistoryEntries(h *viewv1alpha1.PromotionStrategyHistory) int {
	if h == nil {
		return 0
	}
	n := 0
	for _, env := range h.Environments {
		n += len(env.History)
	}
	return n
}

func durationField(d time.Duration) string {
	return d.Round(time.Millisecond).String()
}
