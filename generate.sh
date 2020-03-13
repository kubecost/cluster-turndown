#!/bin/bash
#

DIR=`pwd`
PREFIX=github.com
PACKAGE=${PREFIX}/kubecost/kubecost-turndown
API=turndownschedule
VERSION=v1alpha1

# Cleanup after generation
post_clean() {
    if [ -d "./${PREFIX}" ]; then
        rm -rf ./${PREFIX}
    fi
    if [ -d "./vendor" ]; then
        rm -rf ./vendor
    fi
}

# Clean Generated Code
clean_generated() {
    if [ -d "./pkg/generated" ]; then
        rm -rf ./pkg/generated 
    fi
    if [ -f "./pkg/apis/${API}/${VERSION}/zz_generated.deepcopy.go" ]; then
        rm -f ./pkg/apis/${API}/${VERSION}/zz_generated.deepcopy.go
    fi
    post_clean
}

# Argument processing
clean=0
while [[ "$#" -gt 0 ]]; do case $1 in
  -c|--clean) clean=1; shift;;
  *) echo "Unknown parameter passed: $1"; exit 1;;
esac; shift; done

# Always clean before generation
clean_generated

# If we're cleaning only, exit here
if [ $clean -gt 0 ]; then
    exit
fi

# This is hacky, but works. Since we reference generated code in our application, 
# go mod vendor will actually complain that it doesn't know where the references are.
# In order to pull the code-generator, we copy our go mod/sum up a directory and run
# go mod vendor there, then move the vendor directory back to the root
mkdir tmp
pushd tmp
cp ../go.mod .
cp ../go.sum .
cp -R ../hack .
go mod vendor
popd
mv tmp/vendor .
rm -rf tmp

SCRIPT_ROOT=$(dirname "${BASH_SOURCE[0]}")
CODEGEN_PKG=${CODEGEN_PKG:-$(cd "${SCRIPT_ROOT}"; ls -d -1 ./vendor/k8s.io/code-generator 2>/dev/null || echo code-generator)}

bash ${CODEGEN_PKG}/generate-groups.sh all \
    ${PACKAGE}/pkg/generated \
    ${PACKAGE}/pkg/apis \
    turndownschedule:${VERSION} \
    --output-base "$(dirname "${BASH_SOURCE[0]}")" \
    --go-header-file hack/custom-boilerplate.go.txt

mv ./${PACKAGE}/pkg/generated ./pkg/generated
mv ./${PACKAGE}/pkg/apis/${API}/${VERSION}/* ./pkg/apis/${API}/${VERSION}/

post_clean
