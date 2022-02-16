# Development

## Generating turndown schedule CRD code

To re-generate the code for the defined CRDs, do the following:
```sh
bash generate.sh
```

The script is based on https://github.com/kubernetes/sample-controller, specifically https://github.com/kubernetes/sample-controller/blob/master/hack/update-codegen.sh.

## Cutting a release

1. Update the `VERSION` in the Makefile
2. Update the image version to match in artifacts/cluster-turndown-full.yaml
3. Build and push the new image to GCR (`make release`)
4. Merge the changes
5. Tag a new release off of develop for the new version
