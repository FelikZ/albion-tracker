## Build
FROM --platform=$BUILDPLATFORM golang:1.21.5-alpine3.19 AS build

WORKDIR /app

COPY go.mod ./
COPY go.sum ./
RUN go mod download

COPY *.go ./

ARG TARGETPLATFORM=linux
ARG TARGETARCH=arm
RUN GOOS=$TARGETPLATFORM GOARCH=$TARGETARCH go build -o /albion-tracker

## Deploy
FROM alpine:3.19

WORKDIR /

COPY --from=build /albion-tracker /

ENTRYPOINT ["/albion-tracker", "--server"]