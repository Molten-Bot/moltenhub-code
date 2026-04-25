#!/bin/sh
set -eu

config_dir="${HARNESS_CONFIG_DIR:-/workspace/config}"
run_config_path="${HARNESS_RUN_CONFIG_PATH:-${config_dir}/config.json}"
init_config_path="${HARNESS_INIT_CONFIG_PATH:-${config_dir}/init.json}"
generated_init_path="${HARNESS_GENERATED_INIT_PATH:-/tmp/harness-init-from-env.json}"
template_dir="${HARNESS_TEMPLATE_DIR:-/workspace/templates}"
hub_ui_listen="${HARNESS_HUB_UI_LISTEN-:7777}"

exec_hub() {
    exec harness hub "$@" --ui-listen "${hub_ui_listen}"
}

json_escape() {
    printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'
}

normalize_hub_region() {
    region=$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]' | tr -d '[:space:]')
    case "${region}" in
        ""|na)
            printf 'na'
            ;;
        eu)
            printf 'eu'
            ;;
        *)
            return 1
            ;;
    esac
}

hub_base_url_from_region() {
    region="$1"
    printf 'https://%s.hub.molten.bot/v1' "${region}"
}

resolve_hub_bootstrap_base_url() {
    if [ "${MOLTEN_HUB_URL:-}" != "" ]; then
        case "$(printf '%s' "${MOLTEN_HUB_URL}" | tr -d '[:space:]')" in
            "https://na.hub.molten.bot/v1")
                printf 'https://na.hub.molten.bot/v1'
                return 0
                ;;
            "https://eu.hub.molten.bot/v1")
                printf 'https://eu.hub.molten.bot/v1'
                return 0
                ;;
            *)
                echo "invalid MOLTEN_HUB_URL; expected https://na.hub.molten.bot/v1 or https://eu.hub.molten.bot/v1" >&2
                return 1
                ;;
        esac
    fi

    if ! hub_region=$(normalize_hub_region "${MOLTEN_HUB_REGION:-na}"); then
        echo "invalid MOLTEN_HUB_REGION; expected na or eu" >&2
        return 1
    fi
    hub_base_url_from_region "${hub_region}"
}

hub_config_status() {
    file_path="$1"
    if [ ! -f "${file_path}" ]; then
        return 1
    fi

    node -e '
const fs = require("node:fs");
const [, filePath] = process.argv;
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
  const raw = fs.readFileSync(filePath, "utf8");
  const cfg = JSON.parse(stripLineComments(raw));
  const hubKeys = [
    "base_url",
    "bind_token",
    "agent_token",
    "session_key",
    "agent_harness",
    "agent_command",
    "profile",
    "dispatcher",
    "github_token",
    "openai_api_key",
    "baseUrl",
    "token",
    "sessionKey",
    "timeoutMs",
  ];
  const isHubConfig = hubKeys.some((key) => Object.prototype.hasOwnProperty.call(cfg, key));
  if (!isHubConfig) {
    process.exit(1);
  }
  const rawBaseURL = typeof cfg.base_url === "string" ? cfg.base_url : (typeof cfg.baseUrl === "string" ? cfg.baseUrl : "");
  const baseURL = rawBaseURL.trim();
  if (baseURL !== "") {
    try {
      const parsed = new URL(baseURL);
      const host = parsed.hostname.toLowerCase();
      const path = parsed.pathname.replace(/\/+$/, "");
      if (parsed.protocol !== "https:" || parsed.port !== "" || !host.endsWith(".hub.molten.bot") || host.split(".").length !== 4 || path !== "/v1") {
        process.exit(2);
      }
    } catch (_) {
      process.exit(2);
    }
  }
  process.exit(0);
} catch (_) {
  process.exit(1);
}
' "${file_path}"
}

try_run_hub_from_env() {
    token="${HUB_TOKEN:-${MOLTEN_HUB_TOKEN:-}}"
    if [ "${token}" = "" ]; then
        return 1
    fi

    if ! hub_base_url="$(resolve_hub_bootstrap_base_url)"; then
        return 1
    fi
    session_key="${MOLTEN_HUB_SESSION_KEY:-main}"
    generated_init_dir=$(dirname "${generated_init_path}")
    mkdir -p "${generated_init_dir}"

    {
        printf '{\n'
        printf '  "base_url": "%s",\n' "$(json_escape "${hub_base_url}")"
        printf '  "agent_token": "%s",\n' "$(json_escape "${token}")"
        printf '  "session_key": "%s"\n' "$(json_escape "${session_key}")"
        printf '}\n'
    } > "${generated_init_path}"

    exec_hub --init "${generated_init_path}"
}

if [ -f "${run_config_path}" ]; then
    if hub_config_status "${run_config_path}"; then
        exec_hub --config "${run_config_path}"
    else
        hub_status=$?
        if [ "${hub_status}" = "2" ]; then
            echo "invalid hub config at ${run_config_path}; skipping persisted hub config" >&2
        else
            exec harness run --config "${run_config_path}"
        fi
    fi
fi

if [ -f "${init_config_path}" ]; then
    exec_hub --init "${init_config_path}"
fi

if try_run_hub_from_env; then
    :
fi

if [ "${HARNESS_RUNTIME_CONFIG_PATH:-}" = "" ]; then
    export HARNESS_RUNTIME_CONFIG_PATH="${run_config_path}"
fi

echo "no config file found; starting hub onboarding mode with defaults" >&2
echo "optional run config path: ${run_config_path}" >&2
echo "optional init config path: ${init_config_path}" >&2
echo "or set HUB_TOKEN (or legacy MOLTEN_HUB_TOKEN) and optionally MOLTEN_HUB_REGION=na|eu for remote-hub bootstrap." >&2

exec_hub
