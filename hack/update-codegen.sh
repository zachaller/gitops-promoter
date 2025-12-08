#!/usr/bin/env bash

# Copyright 2024.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_ROOT=$(dirname "${BASH_SOURCE[0]}")/..

# Find the code-generator module
CODEGEN_PKG=$(cd "${SCRIPT_ROOT}" && go list -m -f '{{.Dir}}' k8s.io/code-generator 2>/dev/null)
if [[ -z "${CODEGEN_PKG}" ]]; then
    echo "Error: k8s.io/code-generator not found. Run 'go mod download' first."
    exit 1
fi

source "${CODEGEN_PKG}/kube_codegen.sh"

THIS_PKG="github.com/argoproj-labs/gitops-promoter"

echo "Generating helpers (deepcopy, defaults, conversions)..."
kube::codegen::gen_helpers \
    --boilerplate "${SCRIPT_ROOT}/hack/boilerplate.go.txt" \
    "${SCRIPT_ROOT}/internal/apiserver/apis"

# Handle API violations report
REPORT_FILENAME="${SCRIPT_ROOT}/hack/api-rules/violation_exceptions.list"
UPDATE_REPORT_FLAG=""
if [[ "${UPDATE_API_KNOWN_VIOLATIONS:-}" == "true" ]]; then
    echo "Will update API violations report..."
    UPDATE_REPORT_FLAG="--update-report"
fi

echo "Generating OpenAPI definitions..."
kube::codegen::gen_openapi \
    --output-dir "${SCRIPT_ROOT}/internal/apiserver/generated/openapi" \
    --output-pkg "${THIS_PKG}/internal/apiserver/generated/openapi" \
    --report-filename "${REPORT_FILENAME}" \
    ${UPDATE_REPORT_FLAG} \
    --boilerplate "${SCRIPT_ROOT}/hack/boilerplate.go.txt" \
    "${SCRIPT_ROOT}/internal/apiserver/apis"

echo "Code generation complete!"

