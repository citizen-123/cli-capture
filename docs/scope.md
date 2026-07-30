# Scope

Scope answers two independent yes/no questions with the same rule engine:

| Question | Flags |
|---|---|
| Should this flow **pause** when interception is armed? | `-scope` / `-exclude` |
| Should this TLS host be **decrypted**? | `-mitm` / `-no-mitm` |

Neither affects *recording*. Everything that reaches the proxy is captured and
listed; scope only decides what stops for you, and `-no-mitm` hosts are still
logged as flows — you just get a blind tunnel instead of plaintext.

## Spec grammar

Each flag takes a comma-separated list of specs:

```
[!][field:]pattern
```

| Part | Values |
|---|---|
| `!` | negate this match |
| `field:` | `host` (default) · `path` · `method` · `proto` (or `protocol`) · `body` · `any` |
| `pattern` | `~regex` · `=exact` · contains `*` or `?` → glob · otherwise substring |

`any` joins host, method, path, protocol, and body into one string — a catch-all
text search. Globs are **anchored to the whole field**, and `*` spans `/`, so
`path:/v1/*` matches `/v1/anything/nested` but `path:/v1` (substring) is the
looser form. Regexes are RE2 and unanchored unless you write `^…$`.

```bash
'*.github.com'        # host glob
'path:/v1/*'          # path glob
'method:=POST'        # exact method
'host:~^(api|cdn)\.'  # host regex
'!body:password'      # anything whose body does NOT contain "password"
```

## Postures

The default verdict flips based on whether you gave any includes:

| You pass | Result |
|---|---|
| nothing | intercept everything (once armed) |
| `-scope …` | **allowlist** — intercept only what matches |
| `-exclude …` only | **denylist** — intercept everything except those |
| both | excludes are evaluated first, so they win over overlapping includes |

`-last-match` evaluates every rule and takes the last match instead of
returning on the first. Use it when you want a later rule to override an
earlier one; first-match-wins is the default because it's cheaper and easier to
reason about.

The effective policy is written to `~/.cli-capture/cli-capture.log` at startup
(`intercept scope: …` and `mitm policy: …`) — worth a look when a rule isn't
doing what you expected.

## Two things that surprise people

**Specs are OR, not AND.** Each spec becomes its own rule, and the first
matching rule decides. `-scope 'method:=POST,body:token'` intercepts every POST
*and* everything containing "token" — not just POSTs containing a token. The
engine can AND conditions inside a single rule, but the command line doesn't
expose that yet.

**A flag given twice keeps only the last one.** `-scope a -scope b` is just
`-scope b`. Put every spec in one comma-separated value.

## Examples

```bash
# intercept only GitHub's API, minus a noisy endpoint
cli-capture -scope '*.github.com' -exclude 'path:/telemetry' -- gh pr list

# pause anything that carries a token in the body, anywhere
cli-capture -scope 'body:token' -- some-cli

# watch everything, but never decrypt the bank
cli-capture -no-mitm '*.bank.example' -- some-cli

# decrypt only one host, tunnel the rest blind
cli-capture -mitm 'api.example.com' -- some-cli
```

Interception is still **off until you arm it** with `Ctrl+A i` — scope decides
*what* pauses, not *whether* anything does. See
[intercepting](intercepting.md).
