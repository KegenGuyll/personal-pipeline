# ---- build stage ----
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/deploy-agent .

# ---- runtime stage ----
# `docker:cli` ships the docker CLI; we add the compose v2 plugin so the agent
# can run `docker compose`. The agent talks to the host daemon via the socket.
FROM docker:cli
ARG COMPOSE_VERSION=v2.24.5
RUN apk add --no-cache ca-certificates curl \
 && mkdir -p /usr/local/lib/docker/cli-plugins \
 && curl -fsSL "https://github.com/docker/compose/releases/download/${COMPOSE_VERSION}/docker-compose-linux-$(uname -m)" \
      -o /usr/local/lib/docker/cli-plugins/docker-compose \
 && chmod +x /usr/local/lib/docker/cli-plugins/docker-compose

COPY --from=build /out/deploy-agent /usr/local/bin/deploy-agent
ENTRYPOINT ["deploy-agent"]
