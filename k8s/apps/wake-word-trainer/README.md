# wake-word-trainer

Closes the loop on the custom "Okay GLaDOS" microWakeWord model: the Home
Assistant Voice PE uploads tagged audio, the collector transcribes and trims it
and holds anything doubtful for review, a nightly job retrains once there is
enough new data, and a model that beats the published one replaces it in the
copyparty folder the device's ESPHome build fetches it from.

The design and its reasons are in `docs/wake-word-trainer-plan.md`. The
pipeline itself lives in `tkennedy/micro-wake-word` (`okay_glados/`).

```
Voice PE ──HTTPS──▶ oauth2-proxy ──▶ collector (Deployment) ──▶ clips/ on NFS, SQLite on Longhorn
 POST /capture       (skips SSO        │  review UI, run history, /metrics
 + X-Capture-Token    for /capture)    ├─ processor thread: Whisper, trim, flag
                                       └─ publisher container (UID 3923) ──▶ copyparty public/glados_voice_assistant
                                              ▲ newest promoted run, every minute
nightly CronJob ─ train ──▶ runs/<id>/ on NFS, result to the collector
                  gate, step3-5,
                  N repeats, compare
```

## Workloads

| | What | Runs as |
|---|---|---|
| `wake-word-trainer-collector` | Container `collector` (`wake-word-trainer serve`): capture API, review UI, run history, `/metrics`, the trainer's `/internal` API, and the processing thread. Container `publisher` (`publish --watch`): publishes the newest promoted run that is not published yet | 2400; publisher 3923 |
| `wake-word-trainer-nightly` | CronJob at 02:00 (America/Denver). `train` asks the collector's gate and, if it passes, runs a training run and reports its result | 2400 |
| `wake-word-trainer-seed-<hash>` | One-time steps 1–3 of the pipeline (datasets, TTS, static features). Exits at once when `<work>/.seeded` exists | 2400 |

All three use one image, built from `files/image` (the service package plus the
pipeline from the micro-wake-word GitHub mirror, both Python environments and
the patched piper-sample-generator) and released as
`ghcr.io/teekennedy/k8s-homelab:wake-word-trainer-<version>`.

## Data

| Volume | Where | Holds | Backup |
|---|---|---|---|
| `state` | Longhorn 5 Gi | SQLite: every clip's label, transcript, review state and split; every run's result. The Whisper model | Longhorn `backup` group |
| `work` | Longhorn 150 Gi | ~35 GB datasets, ~45 GB features, checkpoints | none (`ephemeral`): the seed Job rebuilds it |
| `data` | NFS `borg-2:/backups/wake-word-trainer` | `clips/raw/` uploads, `clips/derived/` trimmed clips, `clips/phone/` optional phone recordings, `runs/<id>/` every run's logs, report, curves, selection JSON/NPZ, dataset manifest and model | restic, with the rest of `/storage/nas/backups` |
| `publish` | NFS `borg-2:/backups/copyparty/data/public/glados_voice_assistant` | `okay_glados.tflite`, `okay_glados.json`, `previous/<run-id>/` | restic (copyparty's tree) |

`work` is ReadWriteOnce, so it attaches to one node at a time. The seed and
trainer pods carry `wake-word-trainer.msng.to/work-volume: "true"` and prefer
the node a running one is on, instead of waiting in `ContainerCreating` for it
to finish.

Each clip's split (60/20/20 train/validation/test) is fixed from a hash of its
id when it arrives and never changes, so the test set stays comparable across
runs.

## Processing and review

Each clip is transcribed with word timings (whisper.cpp large-v3-turbo on
CPU) and cut:

| Label | Becomes | Flagged when |
|---|---|---|
| `detected` | lead-in (≤ 1.5 s) + wake word | no "okay + GLaDOS-like word" in the last 2.5 s |
| `false_reject` | lead-in + wake word, found by word timings | zero or several candidates |
| `false_accept` | last 3.2 s (hard negative) + 3 s chunks; held-out splits kept whole as room audio | a wake-word-like phrase is present |
| `true_reject` | 3 s chunks; held-out splits kept whole | a wake-word-like phrase, or near-silent |

Flagged clips are not trained on until someone confirms, relabels or discards
them on `/review`. A relabelled clip is reprocessed under its new label and
counts as confirmed.

## Training runs

The gate passes when, since the last successful run, at least
`gate.minPositives` new usable positives (`detected` + `false_reject`) or
`gate.minFalseAccepts` new `false_accept` clips exist, and no run started in
the last 20 hours. "Train now" on the overview page skips it.

A run: `step3 --only recorded` with the device clips, `trainer.repeats` ×
(`step4 --fresh`, `select_checkpoint --select all --apply`, convert), keep the
repeat with the best validation score, `step5` on it, then `step5 --model` on
the published model at its own cutoff, on the same test data.

It is **promoted** only if the new model holds ≤ `trainer.targetFaph` false
accepts/h at its chosen cutoff **and** misses fewer held-out real clips than
the published model. Publishing is a reconcile loop rather than a step after
training: the publisher asks the collector for the newest promoted run that is
not published, so an older promotion is never published after a newer one,
and a failed publish is retried every 15 minutes. It copies the outgoing pair to
`previous/<run-id>/`, then writes the `.tflite` and finally the `.json`, each
through a temp file and rename. The device picks the model up at its next
ESPHome build.

`publisher.dryRun` (default **true**) rehearses publishing on a scratch
copy of the folder. Set it false once a dry run has been seen to work.

## Device

The firmware component and package are in micro-wake-word,
`okay_glados/esphome/` (see its README). The Voice PE needs:

- The capture token in the ESPHome `secrets.yaml` as `wake_word_capture_token`:

  ```sh
  kubectl -n wake-word-trainer get secret wake-word-trainer-capture-token \
    -o jsonpath='{.data.token}' | base64 -d
  ```

- The firewall rule `allow_esphome_wake_word_capture` in
  `terraform/lan/zone_iot.tf` (applied by hand with `tofu apply`), and DNS for
  `wake-word.msng.to` from the esphome VLAN.

## First deployment

1. Push-mirror `tkennedy/micro-wake-word` to GitHub (Forgejo UI) and merge its
   branch, so the pinned pipeline commit in `files/image/Dockerfile` exists.
2. Apply `nix/modules/nfs-mtls` on borg-2 so the NFS directories exist with
   the right owner.
3. Release the image (any conventional commit under `files/image`).
4. Sync. The seed Job runs for hours; the collector is usable at once.
5. Optional: copy the phone recordings into `clips/phone/` before the seed
   runs, and set `phoneRecordings.enabled: true`.
6. Fetch the capture token, flash the device, and watch `/clips` fill.
7. After the first run, size `trainer.resources` from what it used, pin
   `trainer.nodeSelector`, and turn off `publisher.dryRun`.

## Metrics and alerts

`/metrics` (scraped by the ServiceMonitor): clip counts by label and review
state, queue depth, processing errors, new usable clips toward the gate, last
capture, last gate check, last run status and age, last promotion, and
whether a promotion is waiting to be published. The PrometheusRule alerts on a
queue that does not drain, a dead processor thread, a failed last run, a
promotion unpublished for an hour, a gate that has not run for two days, and
no captures for two days.

## Access

`wake-word.msng.to` on the internal gateway only. oauth2-proxy (Authelia
client `wake-word-trainer`) admits `wake-word-reviewers` (one factor) and
`full-admin` (two factor), and lets `POST /capture` through unauthenticated
for the device, which the collector authenticates by `X-Capture-Token`. The
NetworkPolicy admits only oauth2-proxy, the trainer (to the token-checked
`/internal` API) and the scraper to the collector.

## Development

```sh
cd files/image
uv run pytest
# the image, against a local micro-wake-word checkout (git archive it first:
# the working tree holds ~80 GB of data)
docker buildx build --build-context pipeline=<exported tree> .
```
