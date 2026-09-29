# go-accounting

Personal accounting pipeline: fetch bank transactions from Norwegian banks via the
Enable Banking API (PSD2), store them raw, and turn them into an hledger journal for reporting.

## Planned pipeline

    eb-fetch (per account) → raw/*.jsonl → dedupe/store → export CSV → hledger (rules file) → reports

- Raw bank data is the source of truth. The journal is regenerated from raw data + rules,
  so rule changes apply retroactively. Manual entries (opening balances, cash) live in a
  separate hand-written journal that gets included.
- hledger reads CSV from stdin: `hledger print -f csv:- --rules-file bank.rules`.
- Transfers between own accounts go to a clearing account (`assets:transfers`).

## Layout

    cmd/eb-jwt     signs an RS256 JWT for Enable Banking (app ID + private key)
    cmd/eb-auth    one-time bank authorization; writes sessions/<bank>.json
    cmd/eb-fetch   booked transactions for one account → JSONL on stdout
    internal/eb    shared API client, account-number handling, session file

Build: `go build -o bin/ ./cmd/...`

## Conventions

- Go standard library only; no external dependencies unless there's a strong reason.
- Tools are small CLIs that compose with pipes: data on stdout, messages on stderr, non-zero exit on error.
- Accounts are identified by 11-digit Norwegian account numbers (MOD11-checked), taken
  from the IBAN (`NO` + 2 check digits + number). Never use Enable Banking's account `uid`
  as a persistent ID: it's session-scoped and changes on every re-authorization.
- Only transactions with `status: "BOOK"` are kept. Raw transactions are stored unmodified.
- Dedupe key: account number + `tx.entry_reference` (fall back to a hash of date, amount
  and description if a bank doesn't provide it).

## Enable Banking notes

- Docs: https://enablebanking.com/docs/api/reference/
- JWT: header `kid` = app ID; claims `iss: enablebanking.com`, `aud: api.enablebanking.com`; max TTL 24h.
- The full account details (IBAN etc.) are returned by `POST /sessions`; `GET /sessions/{id}` only
  gives uids. eb-auth saves that mapping so eb-fetch needs no `/details` calls.
- Banks limit unattended requests per consent (often ~4/day). Avoid unnecessary calls.
- Consents expire (bank-specific, typically 90–180 days); re-run eb-auth.

## Testing

Nothing has been run against the real API yet. The tools were tested against a local mock;
use `-base-url` to point them at a mock or the sandbox.

## Security

`sessions/`, private keys and `bin/` must stay out of git. Session files are written 0600.
