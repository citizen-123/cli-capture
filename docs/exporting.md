# Exporting

Everything is written to the config directory — `~/.cli-capture` by default, or
whatever you passed to `-dir`.

| What | Key | File |
|---|---|---|
| Session (all flows, JSON) | `Ctrl+A s` | `session.json` |
| HAR 1.2 (all flows) | `Ctrl+A h` | `capture.har` |
| Flagged flows, as text | `Ctrl+A f` | `flagged.txt` |
| One flow, as text | `s` in the detail view | `flow-<id>.txt` |
| One flow, as a curl command | `c` in the traffic list | `flow-<id>.curl` |

The status bar confirms each write with the path it used.

## curl

`c` renders the selected flow's request as a runnable `curl` command — method,
URL, every header, and the body — so you can take a request out of the capture
and iterate on it in a normal shell.

![Export flow as curl](img/14-export-flow-command.png)

## HAR

`Ctrl+A h` writes the whole capture as HAR 1.2, which browser devtools and most
HTTP tooling can open directly.

![Export session as HAR](img/15-export-har.png)

## Sessions

`Ctrl+A s` saves every captured flow as indented JSON, and `-load` reads one
back into the flow list at startup:

```bash
cli-capture -load ~/.cli-capture/session.json -- some-cli
```

Flags survive the round trip, so you can triage a long capture across several
sittings. Loaded flows are ordinary rows: view, filter, sort, export, and
resend them like anything you just captured.

## These files are secrets

Exports are **verbatim**. That means every `Authorization` header, every
cookie, every session token, and every request body — including an app's system
prompt, if that's what it was sending. HAR and session files carry the full
set; the curl and text exports carry whatever was in that flow.

Nothing is redacted on the way out. Treat the config directory as sensitive:
don't paste these into an issue, and check what's in one before sharing it.
