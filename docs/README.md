# cli-capture docs

Start with the [visual tour](screenshots.md) if you'd rather see it than read
about it — a screenshot of every feature, in order.

| Page | What's in it |
|---|---|
| [Getting started](getting-started.md) | Your first capture, what happens at launch, reading and triaging the flow list, where files land |
| [Keybindings](keybindings.md) | Every shortcut, by context — the same reference the `?` overlay shows |
| [Scope](scope.md) | The `-scope` / `-exclude` / `-mitm` / `-no-mitm` spec grammar and the postures it produces |
| [Intercepting & tampering](intercepting.md) | Arming interception, pausing flows, editing bytes, per-protocol behavior |
| [Repeater & attacks](repeater.md) | Resending, `{{variables}}`, payload lists, and the five attack modes |
| [Exporting](exporting.md) | Sessions, curl, HAR, and plain text — keys, filenames, and what's in them |
| [Architecture](ARCHITECTURE.md) | How the thing is built, for contributors |

The [README](../README.md) is the overview: what cli-capture is for, how to
install it, and the full flag list.

> ⚠️ Everything cli-capture writes is **verbatim** — auth headers, cookies, and
> full request bodies included. Treat sessions and exports as secrets.
