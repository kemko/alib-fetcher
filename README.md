# alib-fetcher

Always-on Go service that fetches new Alib listings and sends them to one or
more Telegram chats. Delivery state is kept independently for each chat in a
bbolt database.

## Configuration

The service reads TOML from `./config.toml`, or from the path passed to
`-config`. See [config.example.toml](config.example.toml).

Global fields and defaults:

- `state_path`: directory containing state databases; default
  `/var/lib/alib-fetcher`. Relative paths are relative to the TOML file.
- `cron_schedule`: `0 0 * * *` (standard five-field cron plus descriptors).
- `timezone`: `Europe/Moscow`.
- `run_on_startup`: `true`.
- `fresh_books`: empty, `age:N`, or `since:YYYY`.
- `http_timeout`: `30s`.
- `alib_max_retries`: `3` additional attempts.
- `message_limit`: `32000`, allowed range `64..32768`.

Each `[[chats]]` entry requires `chat_id` (signed decimal `int64` or a
non-empty `@channel` username) and `telegram_token`. `state_file` is an
optional file name inside `state_path`; otherwise it is `<normalized chat_id>.db`.
Numeric IDs are stored in canonical decimal form and usernames are lowercased.
Absolute paths, path separators, NUL, `.`, and `..` are rejected. Duplicate
normalized IDs and state files are rejected.

Search sources are `categories`, `filters`, and `queries`. Categories retain
the existing ASCII-letter validation. Each filter value makes one independent
Alib form request, in form-field order; each query map makes one request, in
TOML order. All 21 Alib form fields are supported. Values are strings, checkbox
values are `da`, `sumfind` is `1..5`, `sortby` is `0..10`, and `tipfind` uses
the form rubric identifiers. Missing `lday` defaults to `7`. At least one
source is required for every chat. Values are encoded as Windows-1251 before
URL escaping.

The file is decoded strictly: unknown global or chat fields, malformed TOML,
wrong types, invalid IDs, search values, and unsafe state names fail before
network requests or database opens. Legacy environment variables are not read.

## Run

Run one digest for every configured chat:

```bash
alib-fetcher -once -config ./config.toml
```

Run one digest only for a selected chat:

```bash
alib-fetcher -once -chat=-1001234567890 -config ./config.toml
```

Run the scheduled service:

```bash
alib-fetcher -service -config ./config.toml
```

Forget the newest records from one selected database without Telegram or Alib
access:

```bash
alib-fetcher -forget-latest 6 -chat=-1001234567890 -config ./config.toml
```

Exactly one of `-service`, `-once`, and `-forget-latest N` is required.
`-chat` is optional for `-once`, required for `-forget-latest`, and forbidden
for `-service`. No arguments and `-h`/`-help` print help. Argument errors print
help and exit with status 2; configuration and runtime errors exit with status
1. `-once` does not start scheduling, callback polling, or config watching.

The service runs startup and scheduled work independently for each chat and
uses one Telegram SDK client/poller for chats sharing a token. It keeps
pending books and retention state per database. Refresh callbacks, retries,
graceful shutdown, message limits, and delivery ordering follow the same
policy as previous releases.

## Container

Compose runs the service from a read-only root filesystem and mounts the TOML
directory read-only. Replace the file using an atomic rename so the container
sees a complete configuration:

```bash
mkdir -p config
cp config.example.toml config/config.toml
chmod 640 config/config.toml
docker compose up -d
```

The container keeps state in the named `/var/lib/alib-fetcher` volume and runs
as UID/GID 65532. The mounted TOML must be readable by that user and contains
Telegram tokens, so keep the directory private. `ALIB_FETCHER_IMAGE` remains
the only Compose environment override; do not put credentials in Compose.

## Development

Go 1.26.5 is supported. Run the canonical quality gate:

```bash
make verify
```

Use `make coverage` for the 80% total statement-coverage gate. CI uses these
Make targets, runs `govulncheck`, validates Compose, and publishes the image
only from a successful `master` push.
