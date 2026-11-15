"""Small inline SVG charts, so the history pages need no chart service or script."""

import html
import time

W, H = 720, 220
PAD_L, PAD_R, PAD_T, PAD_B = 48, 16, 12, 28

# Series colours are CSS variables, defined for light and dark in base.html.
COLORS = ["var(--c1)", "var(--c2)", "var(--c3)", "var(--c4)", "var(--c5)"]


def _date(ts: float) -> str:
    return time.strftime("%m-%d", time.gmtime(ts))


def _ticks(lo: float, hi: float, n: int = 4) -> list[float]:
    if hi <= lo:
        return [lo]
    return [lo + (hi - lo) * i / n for i in range(n + 1)]


def _frame(y_lo, y_hi, x_lo, x_hi, y_fmt) -> list[str]:
    out = []
    for v in _ticks(y_lo, y_hi):
        y = PAD_T + (H - PAD_T - PAD_B) * (1 - (v - y_lo) / ((y_hi - y_lo) or 1))
        out.append(
            f'<line x1="{PAD_L}" x2="{W - PAD_R}" y1="{y:.1f}" y2="{y:.1f}" class="grid"/>'
            f'<text x="{PAD_L - 6}" y="{y + 4:.1f}" text-anchor="end">{html.escape(y_fmt(v))}</text>'
        )
    for v in _ticks(x_lo, x_hi, 3):
        x = PAD_L + (W - PAD_L - PAD_R) * (v - x_lo) / ((x_hi - x_lo) or 1)
        out.append(
            f'<text x="{x:.1f}" y="{H - 8}" text-anchor="middle">{_date(v)}</text>'
        )
    return out


def _legend(names: list[str]) -> str:
    items = "".join(
        f'<span class="key"><i style="background:{COLORS[i % len(COLORS)]}"></i>'
        f"{html.escape(n)}</span>"
        for i, n in enumerate(names)
    )
    return f'<div class="legend">{items}</div>'


def line_chart(
    series: dict[str, list[tuple[float, float]]],
    y_fmt=lambda v: f"{v:g}",
    highlight: list[float] = (),
    label: str = "",
) -> str:
    """One line per series over time; `highlight` marks x positions (promoted runs)."""
    points = [p for pts in series.values() for p in pts if p[1] is not None]
    if not points:
        return '<p class="empty">No data yet.</p>'
    xs = [p[0] for p in points]
    ys = [p[1] for p in points]
    x_lo, x_hi = min(xs), max(xs)
    if x_hi == x_lo:
        x_lo, x_hi = x_lo - 86400, x_hi + 86400
    y_lo, y_hi = 0.0, max(ys) * 1.1 or 1.0

    def sx(x):
        return PAD_L + (W - PAD_L - PAD_R) * (x - x_lo) / (x_hi - x_lo)

    def sy(y):
        return PAD_T + (H - PAD_T - PAD_B) * (1 - (y - y_lo) / (y_hi - y_lo))

    parts = _frame(y_lo, y_hi, x_lo, x_hi, y_fmt)
    for x in highlight:
        parts.append(
            f'<line x1="{sx(x):.1f}" x2="{sx(x):.1f}" y1="{PAD_T}" y2="{H - PAD_B}" class="mark"/>'
        )
    for i, (name, pts) in enumerate(series.items()):
        pts = [(x, y) for x, y in sorted(pts) if y is not None]
        color = COLORS[i % len(COLORS)]
        if len(pts) > 1:
            d = " ".join(f"{sx(x):.1f},{sy(y):.1f}" for x, y in pts)
            parts.append(
                f'<polyline points="{d}" fill="none" stroke="{color}" stroke-width="2"/>'
            )
        for x, y in pts:
            parts.append(
                f'<circle cx="{sx(x):.1f}" cy="{sy(y):.1f}" r="3.5" fill="{color}">'
                f"<title>{html.escape(name)} {_date(x)}: {html.escape(y_fmt(y))}</title></circle>"
            )
    svg = (
        f'<svg viewBox="0 0 {W} {H}" role="img" aria-label="{html.escape(label)}">'
        + "".join(parts)
        + "</svg>"
    )
    return _legend(list(series)) + svg


def stacked_bars(
    buckets: list[tuple[float, dict[str, int]]], keys: list[str], label: str = ""
) -> str:
    """One stacked bar per time bucket (e.g. week), segments by `keys`."""
    if not buckets:
        return '<p class="empty">No data yet.</p>'
    totals = [sum(c.get(k, 0) for k in keys) for _, c in buckets]
    y_hi = max(totals) * 1.1 or 1.0
    x_lo, x_hi = buckets[0][0], buckets[-1][0]
    if x_hi == x_lo:
        x_hi = x_lo + 7 * 86400
    parts = _frame(0.0, y_hi, x_lo, x_hi, lambda v: f"{v:.0f}")
    inner = W - PAD_L - PAD_R
    bw = max(3.0, inner / max(1, len(buckets)) * 0.7)
    for ts, counts in buckets:
        x = PAD_L + inner * (ts - x_lo) / (x_hi - x_lo) - bw / 2
        x = min(max(x, PAD_L), W - PAD_R - bw)
        base = 0
        for i, k in enumerate(keys):
            n = counts.get(k, 0)
            if not n:
                continue
            y0 = PAD_T + (H - PAD_T - PAD_B) * (1 - (base + n) / y_hi)
            h = (H - PAD_T - PAD_B) * n / y_hi
            parts.append(
                f'<rect x="{x:.1f}" y="{y0:.1f}" width="{bw:.1f}" height="{h:.1f}" '
                f'fill="{COLORS[i % len(COLORS)]}"><title>{_date(ts)} {html.escape(k)}: {n}'
                "</title></rect>"
            )
            base += n
    svg = (
        f'<svg viewBox="0 0 {W} {H}" role="img" aria-label="{html.escape(label)}">'
        + "".join(parts)
        + "</svg>"
    )
    return _legend(keys) + svg
