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

package utils

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	ginkgov2 "github.com/onsi/ginkgo/v2"
)

const (
	// PrometheusOperatorVersion defines the version of the Prometheus operator to install
	PrometheusOperatorVersion = "v0.68.0"
	// PrometheusOperatorURL is the URL template for downloading the Prometheus operator bundle
	PrometheusOperatorURL = "https://github.com/prometheus-operator/prometheus-operator/" +
		"releases/download/%s/bundle.yaml"

	// CertManagerVersion defines the version of cert-manager to install
	CertManagerVersion = "v1.5.3"
	// CertManagerURLTemplate is the URL template for downloading cert-manager
	CertManagerURLTemplate = "https://github.com/jetstack/cert-manager/releases/download/%s/cert-manager.yaml"
)

// warnError writes a warning message to GinkgoWriter for non-critical errors
func warnError(err error) {
	fmt.Fprintf(ginkgov2.GinkgoWriter, "warning: %v\n", err) //nolint:errcheck
}

// InstallPrometheusOperator installs the Prometheus Operator to be used to export the enabled metrics.
func InstallPrometheusOperator() error {
	url := fmt.Sprintf(PrometheusOperatorURL, PrometheusOperatorVersion)
	cmd := exec.Command("kubectl", "create", "-f", url)
	_, err := Run(cmd)
	return err
}

// Run executes the provided command within this context
func Run(cmd *exec.Cmd) ([]byte, error) {
	projectDir, _ := GetProjectDir()
	cmd.Dir = projectDir

	if err := os.Chdir(cmd.Dir); err != nil {
		fmt.Fprintf(ginkgov2.GinkgoWriter, "chdir dir: %s\n", err) //nolint:errcheck
	}

	cmd.Env = append(os.Environ(), "GO111MODULE=on")
	command := strings.Join(cmd.Args, " ")

	fmt.Fprintf(ginkgov2.GinkgoWriter, "running: %s\n", command) //nolint:errcheck
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%s failed with error: (%w) %s", command, err, string(output))
	}

	return output, nil
}

// UninstallPrometheusOperator uninstalls the Prometheus operator
func UninstallPrometheusOperator() {
	url := fmt.Sprintf(PrometheusOperatorURL, PrometheusOperatorVersion)
	cmd := exec.Command("kubectl", "delete", "-f", url)
	if _, err := Run(cmd); err != nil {
		warnError(err)
	}
}

// UninstallCertManager uninstalls the cert-manager
func UninstallCertManager() {
	url := fmt.Sprintf(CertManagerURLTemplate, CertManagerVersion)
	cmd := exec.Command("kubectl", "delete", "-f", url)
	if _, err := Run(cmd); err != nil {
		warnError(err)
	}
}

// InstallCertManager installs the cert-manager bundle.
func InstallCertManager() error {
	url := fmt.Sprintf(CertManagerURLTemplate, CertManagerVersion)
	cmd := exec.Command("kubectl", "apply", "-f", url)
	if _, err := Run(cmd); err != nil {
		return err
	}
	// Wait for cert-manager-webhook to be ready, which can take time if cert-manager
	// was re-installed after uninstalling on a cluster.
	cmd = exec.Command("kubectl", "wait", "deployment.apps/cert-manager-webhook",
		"--for", "condition=Available",
		"--namespace", "cert-manager",
		"--timeout", "5m",
	)

	_, err := Run(cmd)
	return err
}

// LoadImageToKindClusterWithName loads a local docker image to the kind cluster
func LoadImageToKindClusterWithName(name string) error {
	clusterName := "kind"
	if value, ok := os.LookupEnv("KIND_CLUSTER"); ok {
		clusterName = value
	}
	kindOptions := []string{"load", "docker-image", name, "--name", clusterName}
	cmd := exec.Command("kind", kindOptions...)
	_, err := Run(cmd)
	return err
}

// GetNonEmptyLines converts given command output string into individual objects
// according to line breakers, and ignores the empty elements in it.
func GetNonEmptyLines(output string) []string {
	var result []string
	elements := strings.Split(output, "\n")
	for _, element := range elements {
		if element != "" {
			result = append(result, element)
		}
	}

	return result
}

// GetProjectDir returns the directory where the project is located
func GetProjectDir() (string, error) {
	workingDir, err := os.Getwd()
	if err != nil {
		return workingDir, fmt.Errorf("failed to get current working directory: %w", err)
	}
	workingDir = strings.ReplaceAll(workingDir, "/test/e2e", "")
	return workingDir, nil
}
