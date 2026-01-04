# Prometheus Alertmanager template renderer

This go program renders the helpers in helpers.tmpl with some synthetic alert data.
It allows quickly iterating on template formatting.
It prints whitespace using visible characters to aid with debugging formatting issues.

About 95% of the code was written by ChatGPT 5.
The program is missing some of the prometheus template functions and hardcodes the references to templates in ../helpers.tmpl.
It did what it needed to do however, and I think it's worth saving in case I iterate on the template some more.

## Usage

Run the following from this directory:

```console
$ go run render_am_tmpl.go -tmpl ../helpers.tmpl
=== SUBJECT (visible whitespace) ===
[Firing]·HighLatency·(warning)¦

=== MESSAGE (visible whitespace) ===
-·**p99·latency·>·2s**¦⏎
··Gateway·p99·above·SLO¦⏎
··Since·2025-10-19·12:54:19.712122·-0600·MDT¦⏎
··Links:··[runbook](https://runbooks/msng.to/gw-latency)·|·[query](https://prometheus/graph?expr=histogram_quantile(...))¦⏎
¦⏎
-·**p99·latency·back·to·normal**¦⏎
··Since·2025-10-19·12:31:19.712122·-0600·MDT¦⏎
··Links:·[query](https://prometheus/graph?expr=histogram_quantile(...))¦⏎
¦⏎
```

## Background

I created this small utility binary to help me iterate on my Discord alertmanager template.
Originally, my process was:
1. Update the template `helpers.tmpl`.
2. Deploy the changes through Helm / ArgoCD. (takes a few minutes due to multiple hook jobs)
3. Wait for alertmanager to repeat an already firing alert. I temporarily set alertmanager's repeat_interval to 1m so I wouldn't have to wait long.
4. Check discord.

The whole thing took about ~10 minutes, which was a super slow feedback loop.
