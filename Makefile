VERSION=v3.3-SNAPSHOT
REGISTRY=gcr.io
PROJECT_ID=kubecost1
APPNAME=kubecost-turndown

release: clean build push clean

build:
	docker build -t ${REGISTRY}/${PROJECT_ID}/${APPNAME}:${VERSION} .

push:
	gcloud docker -- push ${REGISTRY}/${PROJECT_ID}/${APPNAME}:${VERSION}

clean:
	docker rm -f ${REGISTRY}/${PROJECT_ID}/${APPNAME}:${VERSION} 2> /dev/null || true

.PHONY: release clean build push
