  
FROM golang:latest as build-env

WORKDIR /app
COPY go.mod go.sum ./

# Download all dependencies. Dependencies will be cached if the go.mod and go.sum files are not changed
RUN go mod download

# COPY the source code as the last step
COPY main.go /app/main.go
COPY turndown/ /app/turndown
COPY async/ /app/async

RUN go build -o main .

RUN find . -type f -name '*.go' -exec rm {} +

EXPOSE 9731

CMD ["./main"]