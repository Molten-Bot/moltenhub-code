#!/bin/sh
set -eu

config_dir="${HARNESS_CONFIG_DIR:-/workspace/config}"
if [ "${HOME:-}" = "" ]; then
    export HOME="${config_dir}/home"
fi
if ! mkdir -p "${HOME}"; then
    echo "warning: home directory ${HOME} could not be created; persisted CLI auth settings may fail to save." >&2
fi

if [ ! -w "${config_dir}" ]; then
    echo "warning: config directory ${config_dir} is not writable by uid=$(id -u) gid=$(id -g); persisted onboarding/auth settings may fail to save." >&2
fi

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

detect_config_path_from_args() {
    prev=""
    for arg in "$@"; do
        if [ "${prev}" = "--config" ]; then
            printf '%s' "${arg}"
            return
        fi
        case "${arg}" in
            --config=*)
                printf '%s' "${arg#--config=}"
                return
                ;;
        esac
        prev="${arg}"
    done
}

read_pi_provider_auth_field() {
    raw_json="$1"
    field_name="$2"
    if [ "${raw_json}" = "" ]; then
        return 0
    fi

    node -e '
const [, rawJSON, fieldName] = process.argv;
try {
  const parsed = JSON.parse(String(rawJSON || ""));
  const value = parsed && typeof parsed === "object" ? parsed[fieldName] : "";
  if (typeof value === "string" && value.trim() !== "") {
    process.stdout.write(value.trim());
  }
} catch (_) {
}
' "${raw_json}" "${field_name}"
}

write_pi_auth_json_file() {
    raw_json="$1"
    target_path="$2"
    if [ "${raw_json}" = "" ] || [ "${target_path}" = "" ]; then
        return 0
    fi

    node -e '
const fs = require("node:fs");
const path = require("node:path");
const [, rawJSON, targetPath] = process.argv;
let parsed;
try {
  parsed = JSON.parse(String(rawJSON || "").trim());
  if (typeof parsed === "string") {
    parsed = JSON.parse(parsed);
  }
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed) || Object.keys(parsed).length === 0) {
    throw new Error("expected a non-empty JSON object");
  }
} catch (err) {
  console.error(`warning: invalid PI auth JSON: ${err.message}`);
  process.exit(1);
}
fs.mkdirSync(path.dirname(targetPath), { recursive: true, mode: 0o700 });
fs.writeFileSync(targetPath, JSON.stringify(parsed), { mode: 0o600 });
' "${raw_json}" "${target_path}"
}

read_json_key() {
    json_path="$1"
    keys_csv="$2"
    if [ "${json_path}" = "" ] || [ ! -f "${json_path}" ]; then
        return 0
    fi

    node -e '
const fs = require("node:fs");
const [, jsonPath, keysCSV] = process.argv;
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
  const raw = fs.readFileSync(jsonPath, "utf8");
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
' "${json_path}" "${keys_csv}"
}

init_path="${HARNESS_INIT_CONFIG_PATH:-}"
if [ "${init_path}" = "" ]; then
    init_path="$(detect_init_path_from_args "$@")"
fi
if [ "${init_path}" = "" ]; then
    init_path="${config_dir}/init.json"
fi

config_path="${HARNESS_RUNTIME_CONFIG_PATH:-}"
if [ "${config_path}" = "" ]; then
    config_path="$(detect_config_path_from_args "$@")"
fi
if [ "${config_path}" = "" ]; then
    config_path="${config_dir}/config.json"
fi

if [ "${HARNESS_RUNTIME_CONFIG_PATH:-}" = "" ] && [ "${config_path}" != "" ]; then
    export HARNESS_RUNTIME_CONFIG_PATH="${config_path}"
fi

if [ "${GH_TOKEN:-}" = "" ] && [ "${GITHUB_TOKEN:-}" = "" ]; then
    github_token_from_init="$(read_json_key "${init_path}" "github_token")"
    if [ "${github_token_from_init}" != "" ]; then
        export GITHUB_TOKEN="${github_token_from_init}"
    else
        github_token_from_config="$(read_json_key "${config_path}" "github_token")"
        if [ "${github_token_from_config}" != "" ]; then
            export GITHUB_TOKEN="${github_token_from_config}"
        fi
    fi
fi

if [ "${GH_TOKEN:-}" = "" ]; then
    if [ "${GITHUB_TOKEN:-}" != "" ]; then
        export GH_TOKEN="${GITHUB_TOKEN}"
    fi
fi

if [ "${OPENAI_API_KEY:-}" = "" ]; then
    openai_api_key_from_init="$(read_json_key "${init_path}" "openai_api_key,openaiApiKey,OPENAI_API_KEY")"
    if [ "${openai_api_key_from_init}" != "" ]; then
        export OPENAI_API_KEY="${openai_api_key_from_init}"
    else
        openai_api_key_from_config="$(read_json_key "${config_path}" "openai_api_key,openaiApiKey,OPENAI_API_KEY")"
        if [ "${openai_api_key_from_config}" != "" ]; then
            export OPENAI_API_KEY="${openai_api_key_from_config}"
        fi
    fi
fi

if [ "${AUGMENT_SESSION_AUTH:-}" = "" ]; then
    augment_session_auth_from_init="$(read_json_key "${init_path}" "augment_session_auth,augmentSessionAuth,AUGMENT_SESSION_AUTH")"
    if [ "${augment_session_auth_from_init}" != "" ]; then
        export AUGMENT_SESSION_AUTH="${augment_session_auth_from_init}"
    else
        augment_session_auth_from_config="$(read_json_key "${config_path}" "augment_session_auth,augmentSessionAuth,AUGMENT_SESSION_AUTH")"
        if [ "${augment_session_auth_from_config}" != "" ]; then
            export AUGMENT_SESSION_AUTH="${augment_session_auth_from_config}"
        fi
    fi
fi

pi_provider_auth=""
if [ "${PI_PROVIDER_AUTH:-}" != "" ]; then
    pi_provider_auth="${PI_PROVIDER_AUTH}"
else
    pi_provider_auth="$(read_json_key "${init_path}" "pi_provider_auth,piProviderAuth,PI_PROVIDER_AUTH")"
    if [ "${pi_provider_auth}" = "" ]; then
        pi_provider_auth="$(read_json_key "${config_path}" "pi_provider_auth,piProviderAuth,PI_PROVIDER_AUTH")"
    fi
fi
if [ "${pi_provider_auth}" != "" ]; then
    pi_provider_env_var="$(read_pi_provider_auth_field "${pi_provider_auth}" "env_var")"
    pi_provider_value="$(read_pi_provider_auth_field "${pi_provider_auth}" "value")"
    if [ "${pi_provider_env_var}" != "" ] && [ "${pi_provider_value}" != "" ]; then
        export "${pi_provider_env_var}=${pi_provider_value}"
        export PI_PROVIDER_AUTH="${pi_provider_auth}"
    fi
fi

pi_auth_json=""
if [ "${PI_AUTH_JSON:-}" != "" ]; then
    pi_auth_json="${PI_AUTH_JSON}"
else
    pi_auth_json="$(read_json_key "${init_path}" "pi_auth_json,piAuthJSON,PI_AUTH_JSON")"
    if [ "${pi_auth_json}" = "" ]; then
        pi_auth_json="$(read_json_key "${config_path}" "pi_auth_json,piAuthJSON,PI_AUTH_JSON")"
    fi
fi
if [ "${pi_auth_json}" != "" ]; then
    pi_auth_json_path="${HOME:-/home/node}/.pi/agent/auth.json"
    if ! write_pi_auth_json_file "${pi_auth_json}" "${pi_auth_json_path}"; then
        echo "warning: failed to write PI auth JSON to ${pi_auth_json_path}" >&2
    else
        export PI_AUTH_JSON="${pi_auth_json}"
    fi
fi

git config --global user.name "${GIT_USER_NAME:-moltenhub-bot}"
git config --global user.email "${GIT_USER_EMAIL:-moltenhub-bot@users.noreply.github.com}"

if ! git config --global --get-all url."https://github.com/".insteadOf 2>/dev/null | grep -Fxq "git@github.com:"; then
    git config --global --add url."https://github.com/".insteadOf "git@github.com:"
fi
if ! git config --global --get-all url."https://github.com/".insteadOf 2>/dev/null | grep -Fxq "ssh://git@github.com/"; then
    git config --global --add url."https://github.com/".insteadOf "ssh://git@github.com/"
fi

if [ "${GH_TOKEN:-}" = "" ] && [ "${GITHUB_TOKEN:-}" = "" ]; then
    echo "warning: missing GitHub token: set GITHUB_TOKEN/GH_TOKEN or add github_token to ${config_path} or ${init_path}; continuing so the UI can capture configuration" >&2
else
    if ! gh auth status >/dev/null 2>&1; then
        echo "warning: gh auth status failed; continuing so runtime UI can capture updated GitHub token configuration" >&2
    fi
    if ! gh auth setup-git >/dev/null 2>&1; then
        echo "warning: gh auth setup-git failed; continuing with git fallback configuration" >&2
    fi
fi

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
