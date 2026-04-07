#!/bin/sh
set -eu

if [ "${GH_TOKEN:-}" = "" ] && [ "${GITHUB_TOKEN:-}" = "" ]; then
    echo "missing required environment variable: GITHUB_TOKEN (or GH_TOKEN)" >&2
    exit 21
fi

if [ "${GH_TOKEN:-}" = "" ]; then
    export GH_TOKEN="${GITHUB_TOKEN}"
fi

git config --global user.name "${GIT_USER_NAME:-moltenhub-bot}"
git config --global user.email "${GIT_USER_EMAIL:-moltenhub-bot@users.noreply.github.com}"

if ! git config --global --get-all url."https://github.com/".insteadOf 2>/dev/null | grep -Fxq "git@github.com:"; then
    git config --global --add url."https://github.com/".insteadOf "git@github.com:"
fi
if ! git config --global --get-all url."https://github.com/".insteadOf 2>/dev/null | grep -Fxq "ssh://git@github.com/"; then
    git config --global --add url."https://github.com/".insteadOf "ssh://git@github.com/"
fi

gh auth status >/dev/null
gh auth setup-git >/dev/null

if [ "$#" -eq 0 ]; then
    set -- /usr/local/bin/harness
fi

case "$1" in
    harness)
        shift
        set -- /usr/local/bin/harness "$@"
        ;;
    with-config)
        shift
        set -- /usr/local/bin/with-config "$@"
        ;;
esac

if [ "${1#*/}" = "$1" ] && ! command -v "$1" >/dev/null 2>&1; then
    echo "entrypoint error: command not found: $1" >&2
    exit 127
fi

exec "$@"
