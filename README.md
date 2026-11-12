# Logstream

A local log cockpit built with Go and React. Follow log files on multiple Linux servers over SSH, then search, filter, inspect, and export their events in one workspace.

## Run

Requires **Go 1.26.8+**, **Node.js 22.12+**, npm, and a C compiler for the SQLite driver. Go's automatic toolchain selection can download the required Go version. On macOS, the C compiler comes with Xcode Command Line Tools.

```sh
make setup
make run
```

Open **http://127.0.0.1:8080**. Choose **Try demo** to explore simulated traffic, or **Connect host** to add a real server.

An already built application can be started with:

```sh
./bin/logstream -addr 127.0.0.1:8080 -data data -web web/dist
```

The executable serves `web/dist`; keep that directory alongside your installation, or pass its absolute path with `-web`.

### Windows runner

Extract `bin/logstream-windows-amd64.zip` to a writable folder on a Windows x64 computer, double-click **run-logstream.cmd**, and open **http://127.0.0.1:8080**. Keep the console open; press **Ctrl+C** to stop. The ZIP includes the backend, frontend assets, and instructions. Go, Node.js, and SQLite do not need to be installed on the destination computer. Configuration and rotating backend diagnostics are stored in `data` beside the executable; no local configuration or credentials are included in the package.

Use password or private-key authentication on Windows. The native Windows OpenSSH agent uses a named pipe, which the current Unix-socket agent integration does not support. Remote servers still need GNU `tail` and a POSIX-compatible shell.

To rebuild from macOS/Linux, install the MinGW-w64 cross-compiler, then run:

```sh
make build-windows
```

Set `WINDOWS_CC=/path/to/x86_64-w64-mingw32-gcc` if it is not on your PATH. The build enables CGO for SQLite and links the compiler runtime statically. The archive and its SHA-256 checksum are written to `bin`. The executable is unsigned; Windows runtime behavior must be checked on Windows.

## Connect servers

1. Enter a label, hostname or IP, SSH port, and SSH username.
2. Add absolute log file paths, one per line. Add another host for each additional server.
3. Select SSH agent, private key (with optional passphrase), or password authentication.
4. Inspect the server's SHA-256 host-key fingerprint. Verify it independently, then explicitly accept it.
5. Connect. Each file has its own SSH session, sharing one SSH transport per host.

Target servers must provide GNU `tail`, a POSIX-compatible login shell, and an SSH account allowed to read the selected files. Collectors run `exec tail -n 200 -F -- '<quoted path>'` without a PTY. No root login or passwordless sudo is needed; grant file read access to the SSH account. File paths are shell-quoted, and no arbitrary command input is exposed. GNU `tail -F` follows file names through common rotations and waits for temporarily missing files.

The **SSH agent belongs to the backend process**, not the browser. Start Logstream from a shell with `SSH_AUTH_SOCK` set to use your existing agent. Agent forwarding to target hosts is not enabled.

For CyberArk connections that ask for a password followed by a reason, choose **Password** and fill **Connection reason** with the same reason you normally enter in your SSH client. Password mode supports both SSH password authentication and keyboard-interactive password/reason prompts, including separate rounds. The server's offered authentication methods are recorded as `ssh.auth_methods` at debug level. An unsupported method or missing reason produces an explicit error rather than endless retries.

## Log parsers

Choose **Log parser** when connecting a host (applies to its initial files), or use the pencil beside a source to choose a parser for that file. Existing sources keep **Regular logs**, the original one-line text/JSON parser. Each source has an independent saved selection, so regular and TX files can stream together. Changing a source's parser requires reconnecting its host, just like changing its path; buffered events keep their original interpretation.

**TX application** supports the format with `HH:mm:ss` worker headers, `(thread no:…, pid:…)`, `*** Debugging ***`, request-body continuations, and dashed transaction-summary blocks:

- Worker records extract the worker/component/pool, thread, PID, and explicit severity. JSON on following lines stays with its worker record as `payload`; non-JSON continuations are preserved too.
- Transaction blocks become one event with customer details, pool/thread/bucket, transaction ID, flow and server information, status, total/wait timings, and per-step workers and durations. Successful summaries are `info`; failed/error/aborted/timed-out summaries are `error`. `Start Root` and the final summary are linked only within the same block, without guessing which concurrent worker belongs to a transaction.
- Compact summaries appear in the cockpit. Open an event to inspect structured fields and the full original multiline text. Searches, regular expressions, severity filters, and exports work on the resulting events; text searches include the original payloads and transaction IDs.
- Time-only timestamps stay time-only: no date or remote timezone is assumed. The cockpit remains ordered by backend arrival. A transaction lacking its final summary is marked `partial` rather than assumed successful, including when the initial `tail -n 200` starts midway through a block.
- A new timestamped header, closing separator, end of stream, or one second of inactivity flushes the pending record. Very slowly written records can therefore be split. State is isolated per source and discarded on reconnect; unrelated files or SSH sessions are never joined. Limits of 32 KiB / 256 lines split oversized records into marked partial events. Unrecognized standalone text/JSON uses the regular parser.

The TX parser recognizes log structure; payloads and original log text retain their contents, as with regular logs.

## Cockpit

- Choose **Color theme** in the top bar for Forest, Ocean, Violet, Graphite, or Light. The palette covers the log workspace, dialogs, and event inspector; the selection is remembered in this browser.
- Use the sidebar toggle at the top left of the workspace to hide or restore the navigation and give the log stream the full width. This layout preference is remembered in your browser. Source/group filters and collection keep their state while the sidebar is hidden.
- Choose **New group** (or the **+** beside Server groups), name the group, and select its saved servers. Servers can belong to multiple groups; empty groups can be created before connecting servers. Use **Edit group** to rename a group, change membership, or delete it. Group changes never stop collection, and deleting a group keeps its servers, sources, and buffered logs.
- Select a group in the sidebar or the **All server groups** dropdown to view its servers and logs together. Group filtering applies to live events, retained search results, regex searches, and exports; combine it with source, severity, and text filters. Groups and membership persist across restarts. Up to 64 groups are supported, with unique names (case-insensitive for ASCII).
- Select a host or file in the sidebar; combine it with severity and text filters.
- Use `Cmd/Ctrl+K` to focus search. Toggle `.*` for case-insensitive Go regular expressions (RE2 syntax). Regex search refreshes every 750 ms and never evaluates expressions in the browser.
- Search queries the retained backend buffer and returns up to 3,000 recent matches. Browser display retains up to 3,000 events within its approximate memory budget. Neither counter is an all-time total.
- Pause freezes the current event snapshot; collectors and buffers continue receiving events. Auto-scroll and Jump to latest control the display.
- Select any row to inspect the original message, source, arrival time, parsed timestamp, and JSON fields.
- Export the current filtered view as NDJSON. Clear view hides earlier events in this browser; Reset filters restores retained events.
- Reconnect requests fresh credentials. Stop closes the host's collectors. Remove deletes its saved configuration; buffered events expire normally.
- **Edit** beneath a host updates its label, address, port, SSH username, and pinned host key without deleting its sources. Changing the address or port requires inspecting and explicitly accepting the host key again.
- The **pencil beside a source** edits that file's absolute path. You can also select a source and use the pencil in the log-stream toolbar. The source editor links to its connection settings.
- Saving changed settings stops all collectors for that host, since its files share one SSH connection. The saved notice offers **Reconnect** to apply changes with fresh credentials. Other hosts continue collecting. Source IDs and buffered events are preserved; existing events keep their original host label and file path. Invalid edits, cancellation, and unchanged saves leave collection untouched. A host cannot have two sources with the same path.

## Architecture

```text
Linux files → SSH transports / per-file sessions → normalized events
                                                  ↓
                                    bounded process-local replay buffer
                                                  ↓
                                  SSE batches → virtualized React log list

React → REST → host/source/group configuration → SQLite
```

`cmd/logstream` starts the application. `internal/collector` owns SSH lifecycle and safe tail commands. `internal/stream` normalizes events and implements the replay ring. `internal/store` owns SQLite configuration. `internal/server` exposes REST, SSE, demo traffic, and static assets. `web` contains the React/TypeScript frontend.

The backend retains at most 10,000 events within an estimated 16 MiB event budget. Oversized physical lines are truncated at 32 KiB and marked. Regular mode supports plain text and one JSON object per line; its multiline stack traces remain separate lines. TX mode groups its bounded worker/transaction records as described above. Display order follows backend ingestion, not remote timestamps.

SSE event IDs include a process epoch and sequence. Browser reconnects replay available events after `Last-Event-ID`; stale or expired cursors produce an explicit gap notice. Collectors reconnect with exponential backoff, reopening at the current end of the file to avoid replaying the initial 200 lines. **Logs written during SSH outages can be lost.** There is no durable ingestion cursor or exactly-once delivery guarantee.

## Persistence and access

SQLite stores host labels, addresses, users, pinned fingerprints, file paths and parser selections, and server groups with their membership. Existing databases gain new configuration tables/columns automatically on startup. Passwords, private keys, passphrases, and connection reasons remain in backend memory only while the collector is active. They are never written to SQLite or browser storage. Saved hosts do **not** reconnect automatically after a restart; enter credentials and the reason again. Recent logs and demo traffic are held only in memory.

This release is for **one local user** and binds only to loopback. It checks request host/origin, blocks cross-site requests, and uses an HttpOnly, SameSite cookie for the local API session. It is not a shared team server. Team deployment requires application authentication, authorization, HTTPS, and a deliberate shared credential model before changing this boundary.

## Development and verification

### Backend diagnostics

Backend diagnostics use structured `key=value` records by default, or one JSON object per line with `-log-format json`. Each record has a timestamp, level, stable event name (`msg`), and `service=logstream`. Durations remain readable in both formats. The CLI adds the process ID.

For troubleshooting, stop the existing backend and start the rebuilt executable from the project directory:

```sh
go build -o bin/logstream ./cmd/logstream
./bin/logstream -log-level debug -log-format json -log-file data/backend.log
```

File logging mirrors stderr, appends across restarts, and rotates to `backend.log.1`, `.2`, and `.3` by default. Files have private `0600` permissions. The default limit is 10 MiB per file; one oversized record is kept intact. A file-write failure emits an error to stderr and keeps terminal logging available. Use one backend process per log-file path.

| Flag | Default | Purpose |
| --- | --- | --- |
| `-log-level` | `info` | `debug`, `info`, `warn`, or `error` |
| `-log-format` | `text` | Human-readable text or NDJSON (`json`) |
| `-log-file` | disabled | Optional rotating file; stderr stays enabled |
| `-log-max-size` | `10` | File size in MiB, from 1 to 1024 |
| `-log-backups` | `3` | Retained rotated files, from 1 to 100 |

Useful events:

- **Startup/shutdown:** `app.starting`, `store.opened`, `app.ready`, `app.start_failed`, `app.shutdown_started`, and `app.stopped`. Startup reports the number of saved hosts and sources, and makes clear that credentials are required to reconnect them. Cleanup errors are recorded rather than silently discarded.
- **HTTP:** `http.request` records method, normalized route, response status, duration, bytes, and error detail. Every response has `X-Request-ID`; JSON error responses also carry `requestId`. Failed requests and mutations are visible at `info`; successful polling stays at `debug`. Slow non-stream requests are warnings. Recovered panics produce a generic 500; panic values are omitted, with stack locations available at `debug`.
- **SSH:** `ssh.probe_failed` identifies host-key inspection failures. `ssh.collection_failed` includes `stage`, underlying sanitized `error`, attempt, elapsed duration, `terminal`, and `retry_in` when applicable. Stages distinguish TCP, handshake, session, command execution, reading, and keepalive failures. Transient retries are warnings; terminal failures are errors. Logs carry host, source/file, and `connection_id` where applicable. The initiating HTTP `request_id` follows the worker after the POST completes, allowing one connection attempt to be traced end to end.
- **Data flow:** `ssh.first_event` confirms the file actually produced data. `ssh.tail_stopped` reports received line/byte and stderr counts without copying application lines. `stream.opened` and `stream.closed` report browser stream lifetime, batches, events, replay gaps, and close reason. `stream.replay_gap` identifies unavailable replay history. Normal disconnects and shutdown are not treated as errors.
- **Configuration:** host creation, stop/removal, group creation/update/deletion, demo start/stop, and database failures have their own events. Group events report their ID and member count without listing server details. Internal database errors remain in backend diagnostics; clients receive a generic error and request ID.

For your failing connection, find `ssh.collection_failed`, then follow its `request_id` and `connection_id` through the earlier records. `ssh.remote_stderr` includes up to four sanitized diagnostic lines per file session; additional lines are counted and suppression is reported once. Supplied passwords, keys/passphrases, SSH usernames, application log lines, request bodies, raw URLs/search queries, and cookies are omitted or redacted. Diagnostic files can still include server labels, addresses, file paths, and sanitized remote error text.

A running process does not automatically load code changes. Refresh the browser and reconnect your host after restarting, since credentials remain in memory only.

Keyboard-interactive password and connection-reason prompts are supported in Password mode. OTP/MFA, password-change, repeated password, and unrecognized prompts stop with an explicit error; the backend does not send a password as an answer to those prompts. Connection reasons and challenge responses are redacted from diagnostics. Interactive terminal sessions remain unsupported: a reason requested inside a remote shell after SSH authentication, rather than during keyboard-interactive authentication, requires a different connection flow. Diagnostic logging does not change proxy permissions or authentication requirements.

### Development commands

Start Go and Vite in separate terminals:

```sh
go run ./cmd/logstream
```

```sh
cd web
npm run dev
```

Vite serves **http://127.0.0.1:5173** and proxies `/api` to Go. Fonts and application assets are bundled locally; no external service is required at runtime.

```sh
go test -race ./...
(cd web && npm test && npm run build)
```

Tests cover replay gaps and eviction, concurrent publishing, parsing, database persistence and rollback, API session/origin protection, search and SSE replay, oversized lines, and real SSH protocol connections to an ephemeral local test server. The SSH fixture tests host-key pinning, multiple file sessions, path quoting, streaming, and shutdown without needing production credentials.
