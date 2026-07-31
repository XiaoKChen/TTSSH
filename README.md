# ttssh

An interactive SSH manager for the terminal. Fuzzy-find your SSH key — from a
local folder or straight out of the encrypted key vault — enter the target,
then SSH in or copy files to/from the remote host, all from one flow.

Works on Windows, macOS, and Linux. The fzf-style fuzzy finder is built into the
binary, so the only external requirement is the standard `ssh`/`scp` client
(preinstalled on macOS/Linux, and included with Windows 10+ as the built-in
OpenSSH client).

## Usage

```
ttssh
```

The interactive flow:

1. **Pick a connection** — your 10 most recent connections are offered first
   (most recent on top, with the key name and how long ago you used it), or
   choose **New connection** to set one up:
   - **Pick a key** — fuzzy-search `*.key` files in your key directory (default `~/.ssh`),
     with a preview of each key's path, size, and modified time. The first entry,
     **choose a different folder…**, lets you switch to another folder on the spot
     and optionally save it as the new default.
   - **Enter the target** — username and IP/hostname.
2. **Pick an action** (the menu returns after each action, so you can run several):
   - **SSH into the remote host** — opens an interactive session (`ssh -i <key> user@host`).
   - **Copy a file TO the remote host** — fuzzy-search a local file, enter the remote destination, copies via `scp`.
   - **Copy a file FROM the remote host** — lists remote files over SSH, fuzzy-search one, enter the local destination, copies via `scp`. Falls back to manual path entry if the remote can't be listed.
   - **Switch connection** — go back to the connection list.
   - **Quit**

Press `Esc` inside a copy flow to cancel it and return to the action menu.
Recent entries whose key file has been deleted are pruned automatically;
`ttssh clear-recents` forgets the whole list.

## Key vault (Key-Upload-TUI database)

ttssh can read the Turso/libSQL database that Key-Upload-TUI uploads SSH keys
into, decrypting them client-side with the same v1 contract (HKDF-SHA256
per-unit key, AES-256-GCM, unit id as AAD). Nothing is ever uploaded — ttssh
is read-only against the vault.

When the vault is configured, the key picker lists every stored unit as a
`☁ unit-id` entry next to your local `*.key` files (with fingerprint and
creation date in the preview pane). Picking one decrypts the key to a
private temp file used just for that session and deleted when ttssh exits.
Vault keys also work from the recents list — they are re-fetched on use, so
no key material is persisted between runs.

### Credentials

The vault needs the same three values as the uploader's `.env`:

- `DB_URL` — `libsql://…` or `https://…`
- `DB_TOKEN` — Turso auth token
- `MASTER_KEY_V1_HEX` — the 64-hex-char master key
- `DB_CA_CERT` — optional, path to a private CA root certificate

Each is resolved in this order: **environment variable** → **`.env` in the
current directory** (so running ttssh from the Key-Upload-TUI folder just
works) → **values saved by `ttssh vault setup`**.

`ttssh vault setup` prompts for the values, tests the connection, and stores
them in ttssh's `config.json`. Note the token and master key are stored there
in plain text — protect that file, or stick to environment variables.

### Commands

```
ttssh vault list                          list stored keys (unit, fingerprint, status)
ttssh vault pull                          interactive: multi-select keys, browse to a folder
ttssh vault pull -out D:\keys A6-001-ODU  non-interactive download of specific units
ttssh vault pull -force …                 overwrite existing files without asking
ttssh vault setup                         save credentials to config.json
```

`vault pull` writes each key as `<unit>.key` with owner-only permissions, so
downloaded keys immediately show up in ttssh's normal key scanning. The same
download flow is available inside the interactive key picker
(**⬇ download vault keys to a folder…**).

## Configuring the key directory

Keys must end in `.key`. The default search directory is `~/.ssh`.

The easiest way to change it is from inside the app: pick
**choose a different folder…** at the top of the key list. This opens a
fuzzy-searchable folder browser — type to filter the subfolders of the current
folder, press Enter to step into one, and use **go up (..)** or
**use this folder** to navigate and accept (a preview pane shows how many
`*.key` files each folder contains). **type a path…** is still available for
pasting a full path (`~` is expanded), and going up from a drive root on
Windows offers a drive picker. After picking, choose whether to save the
folder as the default or use it just for this session. If the configured
folder is missing or has no keys, the browser opens automatically instead of
an error.

It can also be set from the command line:

```
ttssh set-key-dir ~/my-keys     # persist a new default
ttssh --key-dir ~/other-keys    # override for a single run
ttssh config                    # show the effective key directory
```

The persisted setting is stored in your OS config directory
(`%AppData%\ttssh\config.json` on Windows, `~/.config/ttssh/config.json` on
Linux, `~/Library/Application Support/ttssh/config.json` on macOS).

## Project layout

```
cmd/ttssh/         CLI entry point: command dispatch, session flow,
                   key picker / folder browser, vault subcommands
internal/config/   persistent settings (config.json), recents, path helpers
internal/ui/       shared terminal styling and prompt helpers
internal/vault/    key-vault client: Turso/libSQL access and v1 decryption
                   (plus the crypto interop tests)
```

## Building

```
go build -o ttssh ./cmd/ttssh
```

Cross-compile for other platforms, e.g. from any OS:

```
GOOS=linux  GOARCH=amd64 go build -o ttssh ./cmd/ttssh
GOOS=darwin GOARCH=arm64 go build -o ttssh ./cmd/ttssh
GOOS=windows GOARCH=amd64 go build -o ttssh.exe ./cmd/ttssh
```

Prebuilt binaries for Linux (amd64), macOS (arm64/amd64), and Windows (amd64)
are attached to each [GitHub release](https://github.com/XiaoKChen/TTSSH/releases)
(built automatically when a `v*` tag is pushed; local builds land in `dist/`) —
everything, including the fuzzy finder and the vault client, is statically
compiled in; the only runtime requirement is `ssh`/`scp` on PATH.

The binary reports its build version in the banner and in `ttssh config`; it is
injected at build time, so release and install-script builds are stamped with
the git tag automatically.

## Installing (run `ttssh` from anywhere)

The install scripts build the binary and put it on your PATH:

```
# Windows (PowerShell, no admin needed)
.\scripts\install.ps1

# Linux / macOS
sh scripts/install.sh
```

- **Windows** installs to `%LOCALAPPDATA%\Programs\ttssh` and adds that folder
  to your user PATH — open a new terminal after the first install.
- **Linux/macOS** installs to `/usr/local/bin` (via sudo if needed), falling
  back to `~/.local/bin` when sudo isn't available.

Pass `-NoBuild` / `--no-build` to install an already-built binary (`./ttssh`,
`ttssh.exe`, or the matching `dist/` binary) instead of compiling from source.
Alternatively, plain `go install ./cmd/ttssh` works too if `$GOPATH/bin` is
already on your PATH.

`go test ./...` includes interop tests that decrypt the exact reference
vectors shared with Key-Upload-TUI's test suite — if those pass, ttssh can
read what the uploader wrote.

## Setup

Requires the Go toolchain (version per `go.mod`) and `ssh`/`scp` on PATH.

```
go mod download
```

## Development

```
go build ./...       # compile everything
go test ./...        # run all tests (includes vault crypto interop vectors)
go test -race ./...  # race detector for concurrent code
go vet ./...         # static analysis
gofmt -l .           # list files needing formatting
```

## Dependencies

- `github.com/charmbracelet/huh` — interactive terminal forms and menus
- `github.com/charmbracelet/lipgloss` — terminal styling for the banner and cards
- `github.com/ktr0731/go-fuzzyfinder` — built-in fzf-style fuzzy finder
