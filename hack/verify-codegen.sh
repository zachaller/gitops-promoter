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

# Create a temporary directory for diffing
DIFFROOT="${SCRIPT_ROOT}/internal/apiserver"
TMP_DIFFROOT="$(mktemp -d)"
cleanup() {
    rm -rf "${TMP_DIFFROOT}"
}
trap cleanup EXIT

# Copy current state
cp -a "${DIFFROOT}"/* "${TMP_DIFFROOT}/"

# Run code generation
"${SCRIPT_ROOT}/hack/update-codegen.sh"

# Check for differences
echo "Checking for differences..."
if ! diff -Naupr "${TMP_DIFFROOT}" "${DIFFROOT}"; then
    echo ""
    echo "Generated files are out of date. Please run 'make generate-apiserver-codegen' and commit the changes."
    exit 1
fi

echo "Generated files are up to date."

