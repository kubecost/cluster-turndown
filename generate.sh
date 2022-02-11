#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail


PACKAGE="github.com/kubecost/cluster-turndown"
API="turndownschedule"
VERSION="v1alpha1"

# Vendor so we have access to the generate-groups.sh script from the desired
# version of k8s.io/code-generator. If vendoring breaks because code is
# referencing generated code that has not yet been generated, refer to the
# old version of this script (commit 5c5e172) which did a little trick to be
# able to vendor if code was not generated.
go mod vendor

SCRIPT_ROOT=$(dirname "${BASH_SOURCE[0]}")
CODEGEN_PKG=${CODEGEN_PKG:-$(cd "${SCRIPT_ROOT}"; ls -d -1 ./vendor/k8s.io/code-generator 2>/dev/null || echo code-generator)}

bash "${CODEGEN_PKG}/generate-groups.sh" all \
    ${PACKAGE}/pkg/generated \
    ${PACKAGE}/pkg/apis \
    ${API}:${VERSION} \
    --go-header-file ./hack/custom-boilerplate.go.txt \
    --output-base ${SCRIPT_ROOT}

# generate-groups.sh creates files at $PACKAGE/pkg/... because of the args
# passed in the above command. These files have to be moved to the correct
# place afterward. Unfortunately, if we try to create the files in the right
# location directly, like with the following:
# bash "${CODEGEN_PKG}/generate-groups.sh" all \
#     ./pkg/generated \
#     ./pkg/apis \
#     ${API}:${VERSION} \
#     --go-header-file ./hack/custom-boilerplate.go.txt \
#     --output-base ${SCRIPT_ROOT}
#
# The import statements in the generated code will be incorrect and cause
# import errors like this:
# pkg/generated/informers/externalversions/generic.go:9:2: local import "./pkg/apis/turndownschedule/v1alpha1" in non-local package
#
# So we have to generate them in ./github.com/kubecost/cluster-turndown/pkg
# and then move them to ./pkg. Frustrating, but it does work. I believe
# this is because the code gen was designed well before go modules and this
# is supposed to work in the GOPATH world.

# Remove old generated code first
rm -r ./pkg/generated

# Then move new generated code to the right place
mv ./${PACKAGE}/pkg/generated ./pkg/generated
mv ./${PACKAGE}/pkg/apis/${API}/${VERSION}/* ./pkg/apis/${API}/${VERSION}

rm -r vendor
