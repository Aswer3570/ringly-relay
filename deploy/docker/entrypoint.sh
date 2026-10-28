#!/bin/sh
# The config directory is mounted from the host (./config), and Docker creates it as root — so the
# daemon's user couldn't write to it. We start as root, hand the directory to the ringly-relay user
# and drop privileges right away. The daemon itself never gets root: sing-box listens on 443 via a
# file capability (see the Dockerfile).
set -e

if [ "$(id -u)" = "0" ]; then
	mkdir -p /etc/ringly-relay
	chown -R ringly-relay:ringly-relay /etc/ringly-relay
	exec su-exec ringly-relay /usr/local/bin/ringly-relay "$@"
fi

exec /usr/local/bin/ringly-relay "$@"
