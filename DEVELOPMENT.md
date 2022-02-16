# Development

## Generating turndown schedule CRD code

To re-generate the code for the defined CRDs, do the following:
```sh
bash generate.sh
```

The script is based on https://github.com/kubernetes/sample-controller, specifically https://github.com/kubernetes/sample-controller/blob/master/hack/update-codegen.sh.

## Cutting a release

1. Call `update-version.sh VERSION`, e.g. `./update-version.sh 1.3.0`
2. Build and push the new image to GCR (`make release`)
3. Merge the changes
4. Tag a new release off of develop for the new version
