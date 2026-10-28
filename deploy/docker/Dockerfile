# sing-box берём из ОФИЦИАЛЬНОГО образа SagerNet, закреплённого по дайджесту: тег можно переписать,
# дайджест — нет. Архитектуру (amd64/arm64/…) Docker выбирает сам. Раньше sing-box в образ не
# попадал вовсе — демон искал его в PATH и мостик не запускался.
#
# Обновление: поменять тег и дайджест вместе. Дайджест — заголовок docker-content-digest ответа
# ghcr.io/v2/sagernet/sing-box/manifests/<тег>. Линейка 1.12 — на ней проверена совместимость
# Reality с iOS-клиентом (Xray); патч-версии формат конфига не меняют.
ARG SINGBOX_IMAGE=ghcr.io/sagernet/sing-box:v1.12.23@sha256:d91f0eb84d31031740b0dd3edd3b9da7b325c2e3c5a181dd9d8e09af24e69767

FROM ${SINGBOX_IMAGE} AS singbox

# Сборка: статический бинарник без CGO.
FROM golang:1.27-alpine AS build
WORKDIR /src
# Кэш зависимостей отдельным слоем.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=docker
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/ringly-relay ./cmd/ringly-relay

FROM alpine:3.20
# libcap — для setcap; su-exec — сбросить root после подготовки каталога конфига (entrypoint.sh).
RUN apk add --no-cache ca-certificates libcap su-exec \
    && adduser -D -H -s /sbin/nologin ringly-relay
COPY --from=singbox /usr/local/bin/sing-box /usr/local/bin/sing-box
COPY --from=build /out/ringly-relay /usr/local/bin/ringly-relay
COPY deploy/docker/entrypoint.sh /usr/local/bin/entrypoint.sh
# Порт 443 привилегированный. Демон работает НЕ от root, поэтому право слушать такие порты выдаём
# самому бинарнику sing-box (file capability) — не всему контейнеру и не через sysctl хоста.
# `cap_add` в Compose этого не делает: у процесса не-root capability из набора контейнера
# не становятся действующими (отсюда `bind: permission denied` на живой установке).
# Последние две команды — проверка ещё при сборке: бинарник запускается и capability на месте.
RUN chmod +x /usr/local/bin/entrypoint.sh \
    && setcap cap_net_bind_service=+ep /usr/local/bin/sing-box \
    && sing-box version \
    && getcap /usr/local/bin/sing-box | grep -q cap_net_bind_service
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["run", "-c", "/etc/ringly-relay/config.toml"]
