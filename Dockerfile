FROM golang:1.26.1-alpine3.23 AS build
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

FROM node:25.8.1-alpine3.23 AS runtime
ENV GIT_TERMINAL_PROMPT=0 \
    HARNESS_AGENT_HARNESS="" \
    HARNESS_AGENT_COMMAND="" \
    HARNESS_AGENTS_SEED_PATH=/opt/moltenhub/library/AGENTS.md \
    HARNESS_WORKSPACE_RAM_BASE=/workspace \
    HARNESS_WORKSPACE_DISK_BASE=/workspace \
    PATH="/usr/local/go/bin:${PATH}"

RUN apk add --no-cache \
        ca-certificates \
        git \
        github-cli \
        jq \
        openssh-client-default \
        ripgrep \
    && npm install --global \
      @openai/codex@latest \
      @anthropic-ai/claude-code@latest \
      @augmentcode/auggie@latest \
      @mariozechner/pi-coding-agent@latest \
      @playwright/test@latest \
    && npm cache clean --force \
    && mkdir -p /workspace/config /workspace/moltenhub-code/tasks \
    && chown -R node:node /workspace
WORKDIR /workspace

COPY --from=build --chmod=755 /out/harness /usr/local/bin/harness
COPY --from=build /usr/local/go /usr/local/go
COPY library /opt/moltenhub/library
COPY skills /opt/moltenhub/skills
COPY --chmod=755 docker/entrypoint.sh /usr/local/bin/entrypoint
COPY --chmod=755 docker/with-config.sh /usr/local/bin/with-config
RUN ln -s /opt/moltenhub/library /workspace/library \
    && ln -s /opt/moltenhub/skills /workspace/skills

VOLUME ["/workspace/config"]

USER node

ENTRYPOINT ["/usr/local/bin/entrypoint"]
CMD ["/usr/local/bin/with-config"]
