ARG AGENT_HARNESS=pi
ARG AGENT_NPM_PACKAGE
ARG AGENT_COMMAND

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
ARG AGENT_HARNESS
ARG AGENT_NPM_PACKAGE
ARG AGENT_COMMAND
ENV GIT_TERMINAL_PROMPT=0
ENV HARNESS_AGENT_HARNESS=${AGENT_HARNESS}
ENV HARNESS_AGENT_COMMAND=${AGENT_COMMAND}
ENV HARNESS_AGENTS_SEED_PATH=/opt/moltenhub/library/AGENTS.md

RUN apk add --no-cache \
        ca-certificates \
        git \
        github-cli \
        jq \
        openssh-client-default \
        ripgrep \
    && agent_harness="$(printf '%s' "${AGENT_HARNESS}" | tr '[:upper:]' '[:lower:]')" \
    && agent_pkg="${AGENT_NPM_PACKAGE}" \
    && if [ -z "${agent_pkg}" ]; then \
        case "${agent_harness}" in \
          codex) agent_pkg='@openai/codex@latest' ;; \
          claude) agent_pkg='@anthropic-ai/claude-code@latest' ;; \
          auggie) agent_pkg='@augmentcode/auggie@latest' ;; \
          pi) agent_pkg='@mariozechner/pi-coding-agent@latest' ;; \
          *) echo "unsupported AGENT_HARNESS: ${AGENT_HARNESS}" >&2; exit 2 ;; \
        esac; \
      fi \
    && npm install --global "${agent_pkg}" \
    && npm cache clean --force

RUN mkdir -p /workspace/config \
    && chown -R node:node /workspace
WORKDIR /workspace

COPY --from=build /out/harness /usr/local/bin/harness
COPY library /opt/moltenhub/library
COPY library /workspace/library
COPY skills /opt/moltenhub/skills
COPY skills /workspace/skills
COPY docker/entrypoint.sh /usr/local/bin/entrypoint
COPY docker/with-config.sh /usr/local/bin/with-config
RUN chmod +x /usr/local/bin/harness /usr/local/bin/entrypoint /usr/local/bin/with-config

VOLUME ["/workspace/config"]

USER node

ENTRYPOINT ["/usr/local/bin/entrypoint"]
CMD ["/usr/local/bin/with-config"]
