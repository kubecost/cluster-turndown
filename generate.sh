#!/bin/bash
#

DIR=`pwd`
PREFIX=github.com
PACKAGE=${PREFIX}/kubecost/kubecost-turndown
API=turndownschedule
VERSION=v1alpha1

go mod vendor

SCRIPT_ROOT=$(dirname "${BASH_SOURCE[0]}")
CODEGEN_PKG=${CODEGEN_PKG:-$(cd "${SCRIPT_ROOT}"; ls -d -1 ./vendor/k8s.io/code-generator 2>/dev/null || echo code-generator)}

if [ -d "./pkg/generated" ]; then
    rm -rf ./pkg/generated 
fi
if [ -f "./pkg/apis/${API}/${VERSION}/zz_generated.deepcopy.go" ]; then
    rm -f ./pkg/apis/${API}/${VERSION}/zz_generated.deepcopy.go
fi

bash ${CODEGEN_PKG}/generate-groups.sh all \
    ${PACKAGE}/pkg/generated \
    ${PACKAGE}/pkg/apis \
    turndownschedule:${VERSION} \
    --output-base "$(dirname "${BASH_SOURCE[0]}")" \
    --go-header-file hack/custom-boilerplate.go.txt

mv ./${PACKAGE}/pkg/generated ./pkg/generated
mv ./${PACKAGE}/pkg/apis/${API}/${VERSION}/* ./pkg/apis/${API}/${VERSION}/
rm -rf ./${PREFIX}
rm -rf ./vendor
