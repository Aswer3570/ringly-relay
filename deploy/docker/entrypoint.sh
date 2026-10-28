#!/bin/sh
# Каталог конфига монтируется с хоста (./config), и Docker создаёт его от root — пользователь демона
# не мог туда записать, и `setup` в контейнере падал. Поэтому стартуем от root, отдаём каталог
# пользователю ringly-relay и сразу сбрасываем права. Сам демон root не получает: порт 443 sing-box
# слушает по file capability (см. Dockerfile).
set -e

if [ "$(id -u)" = "0" ]; then
	mkdir -p /etc/ringly-relay
	chown -R ringly-relay:ringly-relay /etc/ringly-relay
	exec su-exec ringly-relay /usr/local/bin/ringly-relay "$@"
fi

exec /usr/local/bin/ringly-relay "$@"
