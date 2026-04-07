#!/bin/sh
set -eu

config_dir="${HARNESS_CONFIG_DIR:-/workspace/config}"

detect_init_path_from_args() {
    prev=""
    for arg in "$@"; do
        if [ "${prev}" = "--init" ]; then
            printf '%s' "${arg}"
            return
        fi
        case "${arg}" in
            --init=*)
                printf '%s' "${arg#--init=}"
                return
                ;;
        esac
        prev="${arg}"
    done
}

read_init_json_key() {
    init_path="$1"
    keys_csv="$2"
    if [ "${init_path}" = "" ] || [ ! -f "${init_path}" ]; then
        return 0
    fi

    node -e '
const fs = require("node:fs");
const [, initPath, keysCSV] = process.argv;
function stripLineComments(data) {
  let out = "";
  let inString = false;
  let escaped = false;
  for (let i = 0; i < data.length; i++) {
    const ch = data[i];
    if (inString) {
      out += ch;
      if (escaped) {
        escaped = false;
        continue;
      }
      if (ch === "\\") {
        escaped = true;
        continue;
      }
      if (ch === "\"") {
        inString = false;
      }
      continue;
    }
    if (ch === "\"") {
      inString = true;
      out += ch;
      continue;
    }
    if (ch === "/" && i + 1 < data.length && data[i + 1] === "/") {
      while (i < data.length && data[i] !== "\n") {
        i++;
      }
      if (i < data.length && data[i] === "\n") {
        out += "\n";
      }
      continue;
    }
    out += ch;
  }
  return out;
}
try {
  const raw = fs.readFileSync(initPath, "utf8");
  const cfg = JSON.parse(stripLineComments(raw));
  const keys = String(keysCSV || "")
    .split(",")
    .map((k) => k.trim())
    .filter(Boolean);
  for (const key of keys) {
    const value = cfg[key];
    if (typeof value === "string" && value.trim() !== "") {
      process.stdout.write(value.trim());
      process.exit(0);
    }
  }
} catch (_) {
}
' "${init_path}" "${keys_csv}"
}

init_path="${HARNESS_INIT_CONFIG_PATH:-}"
if [ "${init_path}" = "" ]; then
    init_path="$(detect_init_path_from_args "$@")"
fi
if [ "${init_path}" = "" ]; then
    init_path="${config_dir}/init.json"
fi

if [ "${GH_TOKEN:-}" = "" ] && [ "${GITHUB_TOKEN:-}" = "" ]; then
    github_token_from_init="$(read_init_json_key "${init_path}" "github_token,githubToken,GITHUB_TOKEN,gh_token,ghToken,GH_TOKEN")"
    if [ "${github_token_from_init}" != "" ]; then
        export GITHUB_TOKEN="${github_token_from_init}"
    fi
fi

if [ "${GH_TOKEN:-}" = "" ]; then
    if [ "${GITHUB_TOKEN:-}" != "" ]; then
        export GH_TOKEN="${GITHUB_TOKEN}"
    fi
fi

if [ "${OPENAI_API_KEY:-}" = "" ]; then
    openai_api_key_from_init="$(read_init_json_key "${init_path}" "openai_api_key,openaiApiKey,OPENAI_API_KEY")"
    if [ "${openai_api_key_from_init}" != "" ]; then
        export OPENAI_API_KEY="${openai_api_key_from_init}"
    fi
fi

if [ "${GH_TOKEN:-}" = "" ] && [ "${GITHUB_TOKEN:-}" = "" ]; then
    echo "missing required GitHub token: set GITHUB_TOKEN/GH_TOKEN or add GITHUB_TOKEN/github_token to ${init_path}" >&2
    exit 21
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

if [ "${OPENAI_API_KEY:-}" != "" ] && command -v codex >/dev/null 2>&1; then
    if ! printf '%s' "${OPENAI_API_KEY}" | codex login --with-api-key >/dev/null 2>&1; then
        echo "warning: codex login with OPENAI_API_KEY failed; continuing" >&2
    fi
fi

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
