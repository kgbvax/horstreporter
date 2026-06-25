# horstprop — standalone HF link-quality scoring service.
# Build from the repo root:  docker build -f cmd/horstprop/Dockerfile -t horstprop .
FROM golang:1.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/horstprop ./cmd/horstprop

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/horstprop /horstprop
# cty.dat (optional) is mounted at runtime; never baked in.
EXPOSE 9970
ENTRYPOINT ["/horstprop"]
CMD ["-listen", "0.0.0.0:9970"]
