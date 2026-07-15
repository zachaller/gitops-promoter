package utils

import (
	promoterv1alpha1 "github.com/argoproj-labs/gitops-promoter/api/v1alpha1"
)

// StampInstanceIDLabel sets promoter.argoproj.io/instance-id when instanceID is non-nil.
// Callers pass the controller's startup instance ID (settings.Manager.StartupInstanceID).
// Returns labels (never nil).
func StampInstanceIDLabel(labels map[string]string, instanceID *string) map[string]string {
	if labels == nil {
		labels = make(map[string]string)
	}
	if instanceID == nil {
		return labels
	}
	labels[promoterv1alpha1.InstanceIDLabel] = *instanceID
	return labels
}
