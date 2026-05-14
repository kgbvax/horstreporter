#!/bin/bash

echo "Building statically linked binary for Linux x64..."

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -ldflags '-extldflags "-static"' -o horstreporter-linux-x64 .

echo "Build complete: horstreporter-linux-x64"