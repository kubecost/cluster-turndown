VERSION=2.0.0
REGISTRY=gcr.io
PROJECT_ID=kubecost1
APPNAME=cluster-turndown

release: clean build push clean

build:
	docker build -t ${REGISTRY}/${PROJECT_ID}/${APPNAME}:${VERSION} .

push:
	docker push ${REGISTRY}/${PROJECT_ID}/${APPNAME}:${VERSION}

clean:
	docker rm -f ${REGISTRY}/${PROJECT_ID}/${APPNAME}:${VERSION} 2> /dev/null || true

.PHONY: release clean build push
