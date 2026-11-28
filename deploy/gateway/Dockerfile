# The gateway image: one static binary on scratch, and the SSH client builds it hands out. Build from the repository root:
#   docker build -f deploy/gateway/Dockerfile .
# The boot screen is already in the source tree (internal/gateway/boot.gen.html), so the build
# needs Go only.
FROM golang:1.27.4-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS build
# git lets Go record the commit in the binary when the checkout's .git is in the build context.
RUN apk add --no-cache git ca-certificates
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /devbox-gateway ./cmd/devbox-gateway
# The SSH client the setup script downloads from the gateway, built from the same commit.
RUN scripts/build-ssh-client.sh /ssh-client "${VERSION}"

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /devbox-gateway /devbox-gateway
COPY --from=build /ssh-client /usr/share/devbox/ssh-client
# A dedicated unprivileged user (docs/spec/14-security.md). It reads /etc/devbox, writes its
# state directory, and reaches the Incus socket through a supplementary group (compose.yml).
USER 65532:65532
EXPOSE 8080 2222
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --start-interval=1s CMD ["/devbox-gateway", "healthcheck"]
ENTRYPOINT ["/devbox-gateway"]
