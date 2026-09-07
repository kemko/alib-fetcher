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

`fresh_books` controls the optional ✨ marker; it does not filter listings.
`age:N` uses the inclusive threshold `current local year - N`, with `N >= 0`;
`since:YYYY` uses that inclusive year. Empty disables only ✨. The current
year, and the previous year in January, get 🔥; future and unknown years get
🛸. The configured `timezone` determines the current year and month.

Each `[[chats]]` entry requires `chat_id` (signed decimal `int64` or a
non-empty `@channel` username) and `telegram_token`. `state_file` is an
optional file name inside `state_path`; otherwise it is `<normalized chat_id>.db`.
Numeric IDs are stored in canonical decimal form and usernames are lowercased.
Absolute paths, path separators, NUL, `.`, and `..` are rejected. Duplicate
normalized IDs and state files, including existing symlink and hard-link
aliases, are rejected. State-file symlinks must point to existing files.

Search sources are `categories`, `filters`, and `queries`. Categories retain
the existing ASCII-letter validation. Each filter value makes one independent
Alib form request, in form-field order; each query map makes one request, in
TOML order. Values are strings; checkbox values are `da`, `sumfind` is `1..5`,
`sortby` is `0..10`, and `tipfind` uses the form rubric identifiers. The fields
are `author`, `title`, `seria`,
`izdat`, `gorodiz`, `isbnp`, `god1`, `god2`, `cena1`, `cena2`, `sod`, `bsonly`,
`gorod`, `lday`, `noreprint`, `nograv`, `fotoonly`, `minus`, `sumfind`,
`tipfind`, and `sortby`. Missing `lday` defaults to `7`; `filters` values are
independent requests, while each `queries` map is one request containing all
its fields. Sources run in category, form-field, then query order; duplicate
URLs are removed after the first occurrence. At least one source is required
for every chat. Values are encoded as Windows-1251 before URL escaping.

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
1. `-once` does not start scheduling, callback polling, or config watching;
without `-chat` it attempts every recipient and reports errors after all
recipients finish. `-forget-latest` reads only the state mapping, so it needs
no Telegram token or search source and performs no HTTP requests.

The service runs startup and scheduled work independently for each chat and
uses one Telegram SDK client/poller for chats sharing a token. It keeps
pending books and retention state per database. Refresh callbacks, retries,
graceful shutdown, message limits, and delivery ordering follow the same
policy as previous releases. A valid TOML change pauses new work, lets active
digests finish with their old settings, then applies the complete new snapshot;
invalid, deleted, or unreadable files leave the current snapshot active.
The file is checked every second and rechecked after active digests finish.
Polling keeps answering and skipping refresh presses during that wait.
Unchanged settings, including comment-only edits, do not restart work.
Existing chats do not repeat startup digests; newly added chats follow
`run_on_startup`. Retained tokens keep their polling offsets and queued callbacks,
including when `http_timeout` changes. Changing a state path switches databases
without moving history; removed chats keep their files.

To migrate the former single database, change an old `STATE_PATH=/path/state.db`
to `state_path = "/path"` and set `state_file = "state.db"` for the matching
chat. The old database is opened in place; no history is copied or rewritten.

## Container

Compose runs the service from a read-only root filesystem and mounts the TOML
directory read-only. Prepare real tokens and grant the container's GID 65532
read access before atomically renaming the file. On a Linux Docker host:

```bash
install -d -m 0700 config
install -m 0600 config.example.toml config/config.toml.tmp
${EDITOR:-vi} config/config.toml.tmp
sudo chgrp 65532 config config/config.toml.tmp
chmod 0750 config
chmod 0640 config/config.toml.tmp
mv -f config/config.toml.tmp config/config.toml
docker compose up -d
```

The container keeps state in the named `/var/lib/alib-fetcher` volume and runs
as UID/GID 65532. The mounted TOML must be readable by that user and contains
Telegram tokens, so keep the directory private. `ALIB_FETCHER_IMAGE` remains
the only Compose environment override; do not put credentials in Compose.
`config/` and the root `config.toml` are ignored by Git and Docker context,
while the credential-free `config.example.toml` remains trackable.
For later updates, copy the live configuration to a temporary file in the
same directory, edit it, and repeat the group, mode, and rename steps.

## Development

Go 1.26.5 is supported. Run the canonical quality gate:

```bash
make verify
```

Use `make coverage` for the 80% total statement-coverage gate. CI uses these
Make targets, runs `govulncheck`, validates Compose, and publishes the image
only from a successful `master` push.
