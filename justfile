set shell := ["bash", "-euo", "pipefail", "-c"]

op_account := "my.1password.com"

# Must be registered on the app in the Enable Banking control panel.
redirect_url := env("EB_REDIRECT_URL", "https://localhost/callback")

build:
    go build -o bin/ ./cmd/...

# Print a JWT. The private key is streamed from 1Password and never written to disk.
jwt: build
    @EB_APP_ID="$(op read --account {{op_account}} 'op://Private/Enable Banking/application id')" \
        EB_KEY=<(op document get --account {{op_account}} 'EB Private Key') \
        bin/eb-jwt

# List banks available in Norway
banks: build
    EB_JWT="$({{just_executable()}} jwt)" bin/eb-auth -list

# Authorize a bank and save its session, e.g.: just auth DNB dnb
auth bank name: build
    mkdir -p sessions
    EB_JWT="$({{just_executable()}} jwt)" bin/eb-auth -bank "{{bank}}" -redirect-url "{{redirect_url}}" -out "sessions/{{name}}.json" -paste

# Append booked transactions to raw/<account>.jsonl, e.g.: just fetch dnb 1234.56.78903 2026-09-01
fetch session account from to="": build
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p raw
    out="raw/$(tr -d '. ' <<< '{{account}}').jsonl"
    tmp="$(mktemp)"
    trap 'rm -f "$tmp"' EXIT
    # Write to a temp file first so a failed run doesn't append partial output.
    EB_JWT="$({{just_executable()}} jwt)" bin/eb-fetch \
        -session-file "sessions/{{session}}.json" \
        -account "{{account}}" -from "{{from}}" -to "{{to}}" > "$tmp"
    cat "$tmp" >> "$out"
