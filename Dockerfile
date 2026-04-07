ARG AGENT_NPM_PACKAGE=@openai/codex@latest

FROM golang:1.26.1-bookworm AS build
WORKDIR /src
ARG TARGETOS=linux
ARG TARGETARCH=amd64

COPY go.mod ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/harness ./cmd/harness

FROM node:25-bookworm-slim AS runtime
ARG AGENT_NPM_PACKAGE
ENV DEBIAN_FRONTEND=noninteractive
ENV GIT_TERMINAL_PROMPT=0

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        git \
        gh \
        openssh-client \
    && npm install --global "${AGENT_NPM_PACKAGE}" \
    && npm cache clean --force \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

RUN useradd --create-home --shell /bin/sh app
WORKDIR /workspace

COPY --from=build /out/harness /usr/local/bin/harness
COPY docker/entrypoint.sh /usr/local/bin/entrypoint
COPY docker/with-config.sh /usr/local/bin/with-config
RUN chmod +x /usr/local/bin/harness /usr/local/bin/entrypoint /usr/local/bin/with-config

VOLUME ["/workspace/config"]

USER app

ENTRYPOINT ["/usr/local/bin/entrypoint"]
CMD ["/usr/local/bin/with-config"]
