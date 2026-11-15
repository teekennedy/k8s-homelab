"""The review UI's Jinja templates.

Kept in Python rather than as .html files so they travel with the package
wherever its .py files go (CI copies only those).
"""

TEMPLATES = {}

TEMPLATES[
    "_clip_actions.html"
] = r"""<form method="post" action="/clips/{{ clip.id }}/review" class="inline">
  <input type="hidden" name="back" value="{{ back }}">
  {% if clip.status == "error" %}
  <button name="action" value="retry">Retry</button>
  {% else %}
  <button name="action" value="confirm" class="primary">Confirm {{ clip.label }}</button>
  {% endif %}
</form>
<form method="post" action="/clips/{{ clip.id }}/review" class="inline">
  <input type="hidden" name="back" value="{{ back }}">
  <input type="hidden" name="action" value="relabel">
  <select name="label" aria-label="new label">
    {% for l in labels %}<option value="{{ l }}" {% if l == clip.label %}selected{% endif %}>{{ l }}</option>{% endfor %}
  </select>
  <button type="submit">Relabel</button>
</form>
<form method="post" action="/clips/{{ clip.id }}/review" class="inline">
  <input type="hidden" name="back" value="{{ back }}">
  <button name="action" value="discard">Discard</button>
</form>
"""

TEMPLATES["base.html"] = r"""<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{% block title %}Wake word trainer{% endblock %}</title>
<style>
:root {
  --bg: #fafaf8; --fg: #1d1d1b; --muted: #6b6b66; --line: #deded8; --card: #ffffff;
  --accent: #2f5fb3; --warn: #a3471a; --ok: #2e7d4f;
  --c1: #2f5fb3; --c2: #c2672d; --c3: #3f8f5a; --c4: #8a4fb0; --c5: #8f8f2a;
}
@media (prefers-color-scheme: dark) {
  :root {
    --bg: #161615; --fg: #e9e9e4; --muted: #9a9a93; --line: #34342f; --card: #1f1f1d;
    --accent: #7fa6e8; --warn: #e8946a; --ok: #79c495;
    --c1: #7fa6e8; --c2: #e8946a; --c3: #79c495; --c4: #c39be0; --c5: #cfcf6a;
  }
}
* { box-sizing: border-box; }
body { margin: 0; background: var(--bg); color: var(--fg);
  font: 15px/1.45 system-ui, -apple-system, "Segoe UI", sans-serif; }
header { border-bottom: 1px solid var(--line); padding: 10px 16px; display: flex;
  gap: 18px; align-items: baseline; flex-wrap: wrap; }
header strong { font-size: 16px; }
header nav { display: flex; gap: 14px; flex-wrap: wrap; }
header .who { margin-left: auto; color: var(--muted); font-size: 13px; }
main { max-width: 1040px; margin: 0 auto; padding: 16px; }
a { color: var(--accent); }
h1 { font-size: 20px; margin: 8px 0 16px; }
h2 { font-size: 16px; margin: 24px 0 8px; }
table { border-collapse: collapse; width: 100%; font-size: 14px; }
th, td { text-align: left; padding: 6px 8px; border-bottom: 1px solid var(--line); vertical-align: top; }
th { color: var(--muted); font-weight: 600; }
td.num, th.num { text-align: right; font-variant-numeric: tabular-nums; }
.scroll { overflow-x: auto; }
.card { background: var(--card); border: 1px solid var(--line); border-radius: 8px; padding: 12px 14px; margin: 10px 0; }
.muted { color: var(--muted); }
.warn { color: var(--warn); }
.ok { color: var(--ok); }
.tiles { display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: 10px; }
.tile { background: var(--card); border: 1px solid var(--line); border-radius: 8px; padding: 10px 12px; }
.tile b { display: block; font-size: 22px; font-variant-numeric: tabular-nums; }
.tile span { color: var(--muted); font-size: 13px; }
audio { width: 100%; max-width: 420px; height: 34px; }
form.inline { display: inline-flex; gap: 6px; align-items: center; margin: 4px 8px 4px 0; }
button, select { font: inherit; padding: 4px 10px; border-radius: 6px; border: 1px solid var(--line);
  background: var(--card); color: var(--fg); cursor: pointer; }
button.primary { background: var(--accent); border-color: var(--accent); color: var(--bg); }
svg { width: 100%; height: auto; display: block; }
svg text { fill: var(--muted); font-size: 11px; }
svg .grid { stroke: var(--line); }
svg .mark { stroke: var(--ok); stroke-dasharray: 3 3; }
.legend { display: flex; gap: 14px; flex-wrap: wrap; font-size: 13px; color: var(--muted); margin: 4px 0; }
.legend i { display: inline-block; width: 10px; height: 10px; border-radius: 2px; margin-right: 5px; }
pre { white-space: pre-wrap; word-break: break-word; background: var(--card); border: 1px solid var(--line);
  border-radius: 8px; padding: 12px; font-size: 13px; }
img.plot { max-width: 100%; border: 1px solid var(--line); border-radius: 6px; background: #fff; }
.empty { color: var(--muted); }
</style>
</head>
<body>
<header>
  <strong>Wake word trainer</strong>
  <nav>
    <a href="/">Overview</a>
    <a href="/review">Review</a>
    <a href="/clips">Clips</a>
    <a href="/runs">Runs</a>
  </nav>
  <span class="who">{{ user }}</span>
</header>
<main>
{% block content %}{% endblock %}
</main>
</body>
</html>
"""

TEMPLATES["clip.html"] = r"""{% extends "base.html" %}
{% block title %}Clip · Wake word trainer{% endblock %}
{% block content %}
<h1>{{ clip.label }} <span class="muted">· {{ clip.review }}</span></h1>
<div class="card">
  <p class="muted">{{ clip.device }} · {{ clip.received_at | int | ts }} · {{ "%.1f" | format(clip.duration_s) }} s · {{ clip.split }} split · sent as {{ clip.label_sent }}{% if clip.reviewed_by %} · reviewed by {{ clip.reviewed_by }}{% endif %}{% if clip.first_run_id %} · first used by <a href="/runs/{{ clip.first_run_id }}">{{ clip.first_run_id }}</a>{% endif %}</p>
  <audio controls preload="none" src="/clips/{{ clip.id }}/audio"></audio>
  <p>“{{ clip.transcript or "" }}”</p>
  {% if clip.flag_reason %}<p class="warn">{{ clip.flag_reason }}</p>{% endif %}
  {% if clip.error %}<pre>{{ clip.error }}</pre>{% endif %}
  {% with back="/clips/" ~ clip.id %}{% include "_clip_actions.html" %}{% endwith %}
</div>
<h2>Training material</h2>
{% if clip.derived %}
<div class="scroll"><table>
  <tr><th>kind</th><th class="num">from</th><th class="num">to</th><th>listen</th></tr>
  {% for d in clip.derived %}
  <tr><td>{{ d.kind }}</td><td class="num">{{ "%.2f" | format(d.t0) }} s</td><td class="num">{{ "%.2f" | format(d.t1) }} s</td>
    <td><audio controls preload="none" src="/clips/{{ clip.id }}/audio?n={{ loop.index0 }}"></audio></td></tr>
  {% endfor %}
</table></div>
{% else %}<p class="empty">Nothing usable was cut from this clip.</p>{% endif %}
{% if clip.words %}
<h2>Word timings</h2>
<p class="muted">{% for w in clip.words %}{{ w.w }} <small>{{ "%.1f" | format(w.t0) }}</small> {% endfor %}</p>
{% endif %}
{% endblock %}
"""

TEMPLATES["clips.html"] = r"""{% extends "base.html" %}
{% block title %}Clips · Wake word trainer{% endblock %}
{% block content %}
<h1>Clips</h1>
<form method="get" class="inline">
  <select name="label" aria-label="label"><option value="">any label</option>
    {% for l in labels + ("discarded",) %}<option {% if l == label %}selected{% endif %}>{{ l }}</option>{% endfor %}</select>
  <select name="review" aria-label="review state"><option value="">any state</option>
    {% for s in states %}<option {% if s == review %}selected{% endif %}>{{ s }}</option>{% endfor %}</select>
  <button type="submit">Filter</button>
</form>
<div class="scroll"><table>
  <tr><th>received</th><th>device</th><th>label</th><th>review</th><th>split</th><th>transcript</th><th>run</th></tr>
  {% for c in clips %}
  <tr><td><a href="/clips/{{ c.id }}">{{ c.received_at | int | ts }}</a></td><td>{{ c.device }}</td>
    <td>{{ c.label }}</td><td>{{ c.review }}</td><td>{{ c.split }}</td>
    <td class="muted">{{ (c.transcript or "")[:80] }}</td><td>{{ c.first_run_id or "" }}</td></tr>
  {% else %}
  <tr><td colspan="7" class="empty">No clips.</td></tr>
  {% endfor %}
</table></div>
{% endblock %}
"""

TEMPLATES["index.html"] = r"""{% extends "base.html" %}
{% block content %}
<h1>Overview</h1>
<div class="tiles">
  <div class="tile"><b>{{ flagged }}</b><span><a href="/review">flagged for review</a></span></div>
  <div class="tile"><b>{{ queue }}</b><span>waiting to be transcribed</span></div>
  <div class="tile"><b {% if errors %}class="warn"{% endif %}>{{ errors }}</b><span>processing errors</span></div>
  <div class="tile"><b>{{ new.get("detected", 0) + new.get("false_reject", 0) }} / {{ settings.gate_min_positives }}</b><span>new positives toward a run</span></div>
  <div class="tile"><b>{{ new.get("false_accept", 0) }} / {{ settings.gate_min_false_accepts }}</b><span>new false accepts toward a run</span></div>
</div>
{% if processor_error %}<p class="warn">Last processing error: {{ processor_error }}</p>{% endif %}

<h2>Clips</h2>
<div class="scroll"><table>
  <tr><th>label</th>{% for s in states %}<th class="num">{{ s }}</th>{% endfor %}</tr>
  {% for label, row in table.items() %}
  <tr><td><a href="/clips?label={{ label }}">{{ label }}</a></td>
    {% for s in states %}<td class="num">{% if row.get(s) %}<a href="/clips?label={{ label }}&review={{ s }}">{{ row.get(s) }}</a>{% else %}<span class="muted">0</span>{% endif %}</td>{% endfor %}</tr>
  {% endfor %}
</table></div>
<p class="muted">Only <em>auto</em> and <em>confirmed</em> clips are trained on.</p>

<h2>Training</h2>
<div class="card">
  {% if gate %}
  <p>Last gate check {{ gate.checked_at | int | ts }}: <strong>{{ "started a run" if gate.start else "no run" }}</strong> — {{ gate.reason }}.</p>
  {% else %}<p class="muted">The nightly gate has not run yet.</p>{% endif %}
  <form method="post" action="/train-now" class="inline">
    <button class="primary" type="submit">Train now</button>
    <span class="muted">skips the gate; starts within a minute</span>
  </form>
</div>
{% if runs %}
<div class="scroll"><table>
  <tr><th>run</th><th>status</th><th>promoted</th><th>reason</th></tr>
  {% for r in runs %}
  <tr><td><a href="/runs/{{ r.id }}">{{ r.id }}</a></td><td>{{ r.status }}</td>
    <td>{% if r.promoted %}<span class="ok">yes</span>{% elif r.promoted == 0 %}no{% endif %}</td>
    <td class="muted">{{ (r.gate or {}).get("reason", "") }}</td></tr>
  {% endfor %}
</table></div>
{% endif %}
{% endblock %}
"""

TEMPLATES["review.html"] = r"""{% extends "base.html" %}
{% block title %}Review · Wake word trainer{% endblock %}
{% block content %}
<h1>Review</h1>
<p class="muted">Clips whose transcript disagrees with their label, and clips that failed to process. None of them are trained on until resolved. A genuine request cancelled with "stop" shows up here as a <em>false_accept</em> that contains the wake word: relabel it <em>detected</em>.</p>
{% for clip in clips %}
<div class="card">
  <p><a href="/clips/{{ clip.id }}"><strong>{{ clip.label }}</strong></a>
    <span class="muted">· {{ clip.device }} · {{ clip.received_at | int | ts }} · {{ "%.1f" | format(clip.duration_s) }} s · {{ clip.split }}{% if clip.label != clip.label_sent %} · sent as {{ clip.label_sent }}{% endif %}</span></p>
  <audio controls preload="none" src="/clips/{{ clip.id }}/audio"></audio>
  {% if clip.status == "error" %}
  <p class="warn">Processing failed.</p>
  {% else %}
  <p>“{{ clip.transcript or "" }}”</p>
  <p class="warn">{{ clip.flag_reason }}</p>
  {% endif %}
  {% with back="/review" %}{% include "_clip_actions.html" %}{% endwith %}
</div>
{% else %}
<p class="empty">Nothing to review.</p>
{% endfor %}
{% endblock %}
"""

TEMPLATES["run.html"] = r"""{% extends "base.html" %}
{% block title %}Run {{ run.id }} · Wake word trainer{% endblock %}
{% block content %}
{% set res = run.result or {} %}{% set cand = res.get("candidate") or {} %}{% set base = res.get("baseline") or {} %}
<h1>Run {{ run.id }} <span class="muted">· {{ run.status }}</span></h1>
<div class="card">
  <p>Started {{ run.started_at | int | ts }}{% if run.finished_at %}, finished {{ run.finished_at | int | ts }}{% endif %}{% if run.forced %} · started by hand{% endif %}.</p>
  {% if run.gate %}<p class="muted">Gate: {{ run.gate.reason }} ({{ run.gate.new_positives }} new positives, {{ run.gate.new_false_accepts }} new false accepts).</p>{% endif %}
  {% if res.get("decision") %}<p><strong>{{ "Promoted" if run.promoted else "Not promoted" }}:</strong> {{ res.decision }}</p>{% endif %}
  {% if run.published %}<p class="muted">Publisher: {{ run.published }}</p>{% endif %}
  {% if run.error %}<pre class="warn">{{ run.error }}</pre>{% endif %}
</div>
{% if cand %}
<h2>Operating point</h2>
<div class="scroll"><table>
  <tr><th></th><th class="num">cutoff</th><th class="num">false accepts/h</th><th class="num">FRR real clips</th><th class="num">FRR headline</th><th class="num">real clips</th></tr>
  <tr><td>this run</td><td class="num">{{ (cand.get("operating") or {}).get("cutoff") | num }}</td><td class="num">{{ cand.get("faph") | num }}</td><td class="num">{{ cand.get("recorded_frr") | pct }}</td><td class="num">{{ cand.get("frr") | pct }}</td><td class="num">{{ cand.get("recorded_n") }}</td></tr>
  {% if base %}<tr><td>published model</td><td class="num">{{ (base.get("operating") or {}).get("cutoff") | num }}</td><td class="num">{{ base.get("faph") | num }}</td><td class="num">{{ base.get("recorded_frr") | pct }}</td><td class="num">{{ base.get("frr") | pct }}</td><td class="num">{{ base.get("recorded_n") }}</td></tr>{% endif %}
</table></div>
{% if res.get("seeds") %}
<h2>Repeats</h2>
<div class="scroll"><table>
  <tr><th>repeat</th><th class="num">validation score</th><th class="num">best step</th><th></th></tr>
  {% for s in res.seeds %}<tr><td>{{ s.name }}</td><td class="num">{{ s.get("score") | pct }}</td><td class="num">{{ s.get("step", "") }}</td><td>{% if s.name == res.get("best") %}chosen{% endif %}{% if s.get("error") %}<span class="warn">{{ s.error }}</span>{% endif %}</td></tr>{% endfor %}
</table></div>
{% endif %}
{% endif %}
{% if "report/training_progress.png" in files %}
<h2>Training</h2>
<img class="plot" alt="training progress" src="/runs/{{ run.id }}/files/report/training_progress.png">
{% endif %}
{% if "report/roc_curve.png" in files %}
<h2>ROC</h2>
<img class="plot" alt="ROC curve" src="/runs/{{ run.id }}/files/report/roc_curve.png">
{% endif %}
{% if report %}
<h2>Report</h2>
<pre>{{ report }}</pre>
{% endif %}
<h2>Files</h2>
<ul>{% for f in files %}<li><a href="/runs/{{ run.id }}/files/{{ f }}">{{ f }}</a></li>{% else %}<li class="muted">none</li>{% endfor %}</ul>
{% endblock %}
"""

TEMPLATES["runs.html"] = r"""{% extends "base.html" %}
{% block title %}Runs · Wake word trainer{% endblock %}
{% block content %}
<h1>Runs</h1>
<p class="muted">Dashed green lines mark runs whose model was promoted.</p>
<h2>False reject rate on held-out real clips</h2>
<div class="card">{{ frr_chart | safe }}</div>
<h2>False accepts per hour at the chosen cutoff</h2>
<div class="card">{{ faph_chart | safe }}</div>
<h2>Chosen cutoff</h2>
<div class="card">{{ cutoff_chart | safe }}</div>
<h2>Clips received per week, by label</h2>
<div class="card">{{ label_chart | safe }}</div>
<h2>Clips received per week, by review state</h2>
<div class="card">{{ review_chart | safe }}</div>
<h2>All runs</h2>
<div class="scroll"><table>
  <tr><th>run</th><th>status</th><th class="num">FRR real</th><th class="num">published FRR</th><th class="num">FA/h</th><th class="num">cutoff</th><th>promoted</th><th>published</th></tr>
  {% for r in runs %}
  {% set res = r.result or {} %}{% set cand = res.get("candidate") or {} %}{% set base = res.get("baseline") or {} %}
  <tr><td><a href="/runs/{{ r.id }}">{{ r.id }}</a></td><td>{{ r.status }}</td>
    <td class="num">{{ cand.get("recorded_frr") | pct }}</td><td class="num">{{ base.get("recorded_frr") | pct }}</td>
    <td class="num">{{ cand.get("faph") | num }}</td><td class="num">{{ (cand.get("operating") or {}).get("cutoff") | num }}</td>
    <td>{% if r.promoted %}<span class="ok">yes</span>{% elif r.promoted == 0 %}no{% endif %}</td>
    <td>{% if r.published %}{{ "dry run" if r.published.get("dry_run") else ("yes" if r.published.get("published") else "no") }}{% endif %}</td></tr>
  {% else %}
  <tr><td colspan="8" class="empty">No runs yet.</td></tr>
  {% endfor %}
</table></div>
{% endblock %}
"""
