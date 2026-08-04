# pathscope — standalone Pathscope HTTP server.
# Build from the repo root:  docker build -f cmd/pathscope/Dockerfile -t pathscope .
FROM golang:1.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/pathscope ./cmd/pathscope

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/pathscope /pathscope
# Pathscope serves the static UI embedded in the binary; no extra mounts.
EXPOSE 9960
ENTRYPOINT ["/pathscope"]
CMD ["-listen", "0.0.0.0:9960"]
