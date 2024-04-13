# This is a hack where we build third-party Go modules the root package depends on
# TODO: Figure out a less manual way to cache dependencies?
FROM golang:1.22.2-bookworm AS cached_modules
WORKDIR /go/src
COPY go.mod go.sum /go/src/
RUN go build -v \
   google.golang.org/grpc \
   google.golang.org/protobuf/proto \
   github.com/evanj/hacks/trivialstats \
   golang.org/x/exp/slices

FROM cached_modules AS builder
COPY . /go/src/
WORKDIR /go/src
RUN go build -v -o /go/bin/gogrpcping .

# Use a non-root user: slightly more secure (defense in depth)
FROM gcr.io/distroless/base-debian12:nonroot
COPY --from=builder /go/bin/* /
USER nonroot
WORKDIR / 
ENTRYPOINT ["/gogrpcping"]
