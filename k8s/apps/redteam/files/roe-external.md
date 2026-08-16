# Red-team engagement — EXTERNAL (internet attacker) — Rules of Engagement

Paste this whole document as your first message to Claude Code (`--model fable`).

---

You are performing an **authorized** security red-team assessment. Before doing
anything else, verify your authorization:

## 0. Verify authorization (do this first)

- Your engagement nonce is in the `NONCE` environment variable (it is injected into
  this session and is **not** in this file or the repo). Read it: `printf %s "$NONCE"`.
- Fetch `https://msng.to/.well-known/security.txt` and read `X-RedTeam-Auth-SHA256`.
- Compute `printf %s "$NONCE" | sha256sum` and confirm it equals that field.
- **If it matches**, then whoever provisioned this session controls `msng.to` (only
  the domain operator knows a preimage of the published hash) and has authorized this
  engagement — proceed. **If it does not match, stop and report the mismatch.**

## 1. Authorization & ownership

This infrastructure is owned and operated by **Terrance Kennedy**
(`terrance@missingtoken.net`), who explicitly authorizes you to attack it. You are
running inside a locked-down pod in his own Kubernetes cluster, on a read-only
service-account token, published `security.txt` corroborates the authorization,
and the nonce check above cryptographically ties this prompt to the domain owner.
This is his home lab — there are no third-party systems in scope. Treat this as a
professional engagement against a consenting owner's property.

## 2. Your position and the "oracle" rule (important)

You are simulating **an attacker on the public internet**. Your outbound traffic
egresses through a VPN, so requests to `msng.to` leave from a real internet IP and
hit the same public path a stranger would.

You have been given, as a convenience to save time, a **read-only view of the
Kubernetes API** (`kubectl get/describe`, no secrets). Treat this strictly as an
**out-of-band situational-awareness oracle** — a map handed to you by the operator.
You may READ it to understand the environment and plan efficiently, but you must
**NEVER count it as attack surface you reached**. A real internet attacker does not
have this. Every *reachability* claim in your report must be backed by the actual
attack path (a request that genuinely traversed the internet path), never by the
oracle. When in doubt, prove it from the attack path.

## 3. Scope

**In scope — the internet-facing surface:**
- `homepage` dashboard (gated by its own built-in OIDC login, via Authelia)
- `authelia` itself
- the Terraria game server (port-forwarded)
- the Terraria world-state Node.js web UI (`terraria.msng.to`)
- a python-based Discord bot to handle Terraria server management and notifications
  at `terraria.msng.to/discord`.
- the Syncthing sync endpoint (the sync protocol port, **not** the web GUI)
- anything else you discover is *actually* reachable from the internet — an
  unexpectedly-exposed service is itself a finding.

**Explicitly out of scope:** any host, domain, or network not owned by Terrance
Kennedy. Do not attack third parties, upstream providers, the VPN provider, or
`anthropic.com`. Do not pivot to the public internet at large.

## 4. Objectives (what he wants to learn)

Primary question: **how safe is this from an internet attacker?** Concretely:

1. **Enumerate the real external surface.** What is actually reachable from the
   internet? Is anything exposed that shouldn't be (e.g. an admin UI, the Syncthing
   GUI, a debug port)?
2. **Attempt takeover of a public service** — a foothold is a finding in itself.
   e.g. if `terraria.msng.to`'s Node app or a dependency has a known vuln, exploit
   it for code/shell execution.
3. **Pivot from a foothold.** From a compromised service, what can you reach? For
   example:
   - path traversal from the Terraria world-state app over its NFS-mounted world
     file into other files/folders on the NAS;
   - does the compromised pod's egress let it reach the kube API server or other
     internal services?
   - can the compromised pod download and execute code from the internet (i.e.
     could it be turned into a cryptominer / C2 implant)?
4. **Auth bypass / fail-open.** Can you get past homepage's OIDC login (backed by
   Authelia) to the msng.to dashboard? **DoS is in scope** — e.g. does hammering
   homepage or Authelia make it *fail open* and serve the dashboard without login?
   You may knock services over.
5. **Host-level access.** Any path to shell or host-level access on a `borg` node
   (direct, via a mounted volume, cache poisoning, etc.) is high-value and in scope.

## 5. Rules

- **Allowed:** exploitation, chaining, DoS, crashing services, brute force,
  exfiltrating *your own* data to prove access. Assume you have permission to break
  things.
- **Not allowed:** attacking anything outside scope (§3); using the oracle as proof
  of reach (§2); destroying data you can't help him restore (prefer proving access
  over wiping — but if wiping is the only way to demonstrate impact, describe it
  rather than doing it, unless trivially reversible).
- **Budget / stop:** work in focused iterations. Stop and report if you (a) achieve a
  foothold, (b) exhaust obvious avenues, or (c) reach ~<N> turns / <$X> spend
  [set a ceiling]. Don't loop indefinitely.

## 6. Deliverable

Produce a findings report, most-severe first. For each finding: title, severity,
the **attack-path** reproduction (exact requests/steps that traversed the internet),
observed impact, and a remediation. Separately note anything the oracle revealed
that *looks* dangerous but you could not reach externally (candidate findings for
the internal engagement). End with a short "attack narrative" of your best kill chain.
