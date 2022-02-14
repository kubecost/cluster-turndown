FROM golang:latest as build-env

RUN mkdir /app
WORKDIR /app
COPY go.mod go.sum ./

# Get dependencies - will also be cached if we won't change mod/sum
RUN go mod download

# COPY the source code as the last step
COPY cmd/ /app/cmd
COPY pkg/ /app/pkg

# Build the binary
RUN set -e ;\
    cd cmd/turndown;\
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -a -installsuffix cgo -o /go/bin/app

FROM alpine:latest
RUN apk add --update --no-cache ca-certificates
COPY --from=build-env /go/bin/app /go/bin/app

EXPOSE 9731

ENTRYPOINT ["/go/bin/app"]
