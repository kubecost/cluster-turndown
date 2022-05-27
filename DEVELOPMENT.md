# Development

## Building development images

```sh
export TDTAG="dockerregistry.example.com/cluster-turndown:X.Y.Z"
docker build -t "${TDTAG}" .
docker push "${TDTAG}"

# Then update the image in your cluster
kubectl set image \
    -n turndown \
    deployment/cluster-turndown \
    cluster-turndown="${TDTAG}"
```

## Generating turndown schedule CRD code

To re-generate the code for the defined CRDs, do the following:
```sh
bash generate.sh
```

The script is based on https://github.com/kubernetes/sample-controller, specifically https://github.com/kubernetes/sample-controller/blob/master/hack/update-codegen.sh.

## Cutting a release

1. Call `update-version.sh VERSION`, e.g. `./update-version.sh X.Y.Z`
2. Build and push the new image to GCR (`make VERSION=X.Y.Z release`)
3. Merge the changes
4. Tag a new release off of `develop` for the new version
5. Manually upload `artifacts/cluster-turndown-full.yaml` and `scripts/gke-create-service-key.sh` to the GitHub Release created by the tag
