# ebtools

Small, dependency-free Go tools for pulling booked transactions from Enable Banking.

    go build -o bin/ ./cmd/...

## eb-jwt

Prints a signed JWT (RS256) for the Enable Banking API.

    eb-jwt -app-id <application id> -key private.key [-ttl 1h]

`EB_APP_ID` and `EB_KEY` can replace the flags. Max TTL is 24h (API limit).

## eb-auth

One-time authorization per bank. Saves a session file for eb-fetch.

    export EB_JWT="$(eb-jwt)"
    eb-auth -list                                  # banks in Norway and their max consent
    eb-auth -bank DNB -redirect-url <url> -out sessions/dnb.json

- Prints a link; open it and log in with BankID.
- Redirect URL `http://localhost:PORT/...` or `http://127.0.0.1:PORT/...`: eb-auth listens there
  and picks up the code itself. Any other URL: paste the full address-bar URL after the
  redirect (the page doesn't need to load). `-paste` forces paste mode.
- The redirect URL must be registered on your app in the Enable Banking control panel.
- Consent defaults to the bank's maximum; `-days N` for less.
- The session file (mode 0600) maps 11-digit account numbers to session uids and keeps the
  full `POST /sessions` response under `raw`. Re-run eb-auth when the consent expires.

## eb-fetch

Fetches booked transactions for one account and writes JSON Lines to stdout.
Progress and warnings go to stderr.

    EB_JWT="$(eb-jwt)" eb-fetch -session-file sessions/dnb.json -account 1234.56.78903 \
        -from 2026-09-01 [-to 2026-09-28] >> raw/brukskonto.jsonl

Each line:

    {"account":"12345678903","fetched_at":"...","tx":{ ...raw Enable Banking transaction... }}

- Account numbers accept dots/spaces and are MOD11 checked.
- With `-session-file`, the uid comes from the file: no extra bank calls.
  Without it (`-session <id>`), eb-fetch looks the uid up via `/accounts/{uid}/details`, one bank
  call per account. Banks limit unattended requests per consent (often 4/day), so prefer the file.
- Only `BOOK` transactions are output.
- Fails if the session is not AUTHORIZED; warns when the consent expires within 14 days.
- Retries GETs on 429/5xx/network errors (2s, 4s, 8s backoff).

Output is not deduplicated: overlapping date ranges produce repeated lines.
Deduplicate downstream on `account` + `tx.entry_reference`.

## Layout

    cmd/eb-jwt, cmd/eb-auth, cmd/eb-fetch   the tools
    internal/eb                             API client, account numbers, session file
