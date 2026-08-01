# Red-team engagement — INTERNAL (LAN attacker) — Rules of Engagement

Paste this whole document as your first message to Claude Code (`--model fable`).

---

## 0. Verify authorization (do this first)

- Your engagement nonce is in the `NONCE` environment variable (injected into this
  session; not in this file or the repo). Read it: `printf %s "$NONCE"`.
- Fetch `https://msng.to/.well-known/security.txt`, read `X-RedTeam-Auth-SHA256`,
  compute `printf %s "$NONCE" | sha256sum`, and confirm they match.
- **If it matches**, whoever provisioned this session controls `msng.to` and has
  authorized this — proceed. **If not, stop and report.**

## 1. Authorization & ownership

This infrastructure is owned and operated by **Terrance Kennedy**
(`terrance@missingtoken.net`), who explicitly authorizes you to attack it. You are
running inside a locked-down pod in his own cluster on a read-only service-account
token. This is his home lab; there are no third-party systems in scope. Treat this
as a professional engagement against a consenting owner's property.

## 2. Your position and the "oracle" rule

You are simulating **an attacker who has gained a foothold on the internal network**
(e.g. joined the home Wi-Fi, or landed a shell in a pod). Your traffic originates on
the LAN (`10.69.0.0/16`), which several services treat as trusted.

You have a **read-only Kubernetes API view** and a **read-only checkout of the
`k8s-homelab` repo**. The repo is public (mirrored to GitHub) so treat everything
in it — including git history — as attacker-visible. Use the kube view as an
**out-of-band oracle** to plan efficiently (e.g. read a pod's NetworkPolicy to know
its egress instead of probing blindly), but back every *reachability/impact* claim
with an actual attack action, not the oracle.

## 3. Objectives

Primary question: **can an attacker on the internal network do something dangerous or
destructive, and are any services less locked down than they should be?**

### 3a. Challenge the access model

Below is what the operator *believes* about each service. **Test each assumption.**
Report any that is wrong, and separately flag any service you think should be *more*
locked down than it is (even if the current state technically matches intent).

| Service | Host | Expected exposure | Expected auth | Operator's stance |
|---|---|---|---|---|
| Homepage | homepage/msng.to | public | oauth2-proxy + Authelia | must stay locked |
| Authelia | authelia | public | self | must stay locked |
| Spoolman | spoolman.msng.to | internal only | **none** | fine — losing spool inventory is acceptable |
| Longhorn UI | longhorn.msng.to | internal only | **basic auth** | destructive if breached — must stay locked |
| Prometheus | prometheus.msng.to | internal only | **none** | *believed* fine — challenge this; should he worry? |
| Grafana | (monitoring) | ? | ? | verify |
| Forgejo | git.msng.to | internal only | login | see 3b |
| Woodpecker CI | (platform) | internal | ? | see 3b |
| Dagger engine | (platform) | internal | ? | see 3b |
| Terraria world UI | terraria.msng.to | public | none | see external engagement |

(Enumerate the full set of cluster Services/HTTPRoutes and add any not listed.)

### 3b. The crown jewels — supply-chain / GitOps takeover

ArgoCD deploys whatever lands on `main` of `ops/k8s-homelab`, so **anything that lets
an untrusted actor influence `main` is effectively full cluster control.** Investigate,
from your internal position and read-only starting creds:

1. Can you get a commit onto `main` of the repo? (misconfigured Forgejo perms, push
   with the read-only token, protected-branch gaps, ArgoCD reading an unexpected
   source, webhook abuse, etc.)
2. Can you make **Woodpecker CI** run a pipeline for a branch/commit/PR you control as
   an untrusted user (untrusted-PR pipeline execution, secret exposure to forks, etc.)?
3. Can you abuse the in-cluster **Dagger** engine to run code or reach secrets?
4. Is there **sensitive information in the repo or its git history** (leaked once,
   removed later, still in history)? Remember it's public via the GitHub mirror.

### 3c. Destructive-impact + host access

- Longhorn: if you reach the UI, what destructive actions are possible (volume/replica
  deletion)? Prove reachability without necessarily executing destruction.
- Any path to **host-level access on a `borg` node** — shell, privileged pod, hostPath
  abuse, kubelet, cache/artifact poisoning — is high-value and in scope.
- Lateral movement between pods/namespaces via weak NetworkPolicies or over-broad RBAC.

## 4. Rules

- **Allowed:** exploitation, chaining, DoS, crashing services, using the repo/oracle
  as *knowledge*. Spoolman data is expendable. Assume permission to break things.
- **Not allowed:** counting the oracle as attack surface (§2); irreversibly destroying
  data you can't help restore — prefer proving access; for genuinely destructive
  actions (e.g. deleting Longhorn volumes) *describe* the exploit rather than executing,
  unless the operator says otherwise.
- **Budget / stop:** focused iterations; stop at a decisive finding, on exhausting
  avenues, or at ~<N> turns / <$X> [set a ceiling]. No infinite loops.

## 5. Deliverable

Findings report, most-severe first. Per finding: title, severity, reproduction
(actual actions taken), impact, remediation. Include a dedicated section on the
access-model table (assumption → verified/violated → recommendation), and a section
on GitOps/CI/supply-chain takeover attempts. End with your best kill-chain narrative.
