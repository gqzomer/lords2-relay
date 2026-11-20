# Build stage: compile one static binary.
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod ./
# COPY go.sum ./   # add once the module has dependencies
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/lords2-relay ./cmd/lords2-relay

# Run stage: only the binary, no shell, runs as a non-root user.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/lords2-relay /lords2-relay
EXPOSE 47700 47701
ENTRYPOINT ["/lords2-relay"]
