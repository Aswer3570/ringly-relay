# sing-box comes from the OFFICIAL SagerNet image, pinned by digest: a tag can be rewritten, a digest
# can't. Docker picks the architecture (amd64/arm64/…) itself.
#
# To update: change the tag and the digest together. The digest is the docker-content-digest header
# of ghcr.io/v2/sagernet/sing-box/manifests/<tag>. The 1.12 line is the one tested for Reality
# compatibility with the iOS client (Xray); patch releases don't change the config format.
ARG SINGBOX_IMAGE=ghcr.io/sagernet/sing-box:v1.12.23@sha256:d91f0eb84d31031740b0dd3edd3b9da7b325c2e3c5a181dd9d8e09af24e69767

FROM ${SINGBOX_IMAGE} AS singbox

# Build: a static binary without CGO.
FROM golang:1.27-alpine AS build
WORKDIR /src
# Dependency cache in a separate layer.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=docker
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/ringly-relay ./cmd/ringly-relay

FROM alpine:3.20
# libcap for setcap; su-exec to drop root after preparing the config directory (entrypoint.sh).
RUN apk add --no-cache ca-certificates libcap su-exec \
    && adduser -D -H -s /sbin/nologin ringly-relay
COPY --from=singbox /usr/local/bin/sing-box /usr/local/bin/sing-box
COPY --from=build /out/ringly-relay /usr/local/bin/ringly-relay
COPY deploy/docker/entrypoint.sh /usr/local/bin/entrypoint.sh
# Port 443 is privileged. The daemon does NOT run as root, so the right to listen on such ports is
# given to the sing-box binary itself (a file capability) — not to the whole container and not via a
# host sysctl. `cap_add` in Compose doesn't do this: for a non-root process the container's
# capabilities don't become effective (hence `bind: permission denied` on a live install).
# The last two commands check at build time that the binary runs and the capability is set.
RUN chmod +x /usr/local/bin/entrypoint.sh \
    && setcap cap_net_bind_service=+ep /usr/local/bin/sing-box \
    && sing-box version \
    && getcap /usr/local/bin/sing-box | grep -q cap_net_bind_service
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["run", "-c", "/etc/ringly-relay/config.toml"]
