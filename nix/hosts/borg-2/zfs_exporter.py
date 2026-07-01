import json
import os
import re
import subprocess
import sys
from datetime import datetime

ZPOOL = os.environ["ZPOOL_BIN"]
TEXTFILE_DIR = os.environ["TEXTFILE_DIR"]
OUTPUT = os.path.join(TEXTFILE_DIR, "zfs.prom")

errors = []


def run_json(*args):
    r = subprocess.run([ZPOOL] + list(args), capture_output=True, text=True)
    cmd = " ".join(args)
    if r.returncode != 0:
        msg = f"zpool {cmd} failed (exit {r.returncode}): {r.stderr.strip()}"
        print(msg, file=sys.stderr)
        errors.append(msg)
        return None
    try:
        return json.loads(r.stdout)
    except json.JSONDecodeError as e:
        msg = f"zpool {cmd} returned invalid JSON: {e}"
        print(msg, file=sys.stderr)
        errors.append(msg)
        return None


def run_tabular(*args):
    r = subprocess.run([ZPOOL] + list(args), capture_output=True, text=True)
    if r.returncode != 0:
        cmd = " ".join(args)
        msg = f"zpool {cmd} failed (exit {r.returncode}): {r.stderr.strip()}"
        print(msg, file=sys.stderr)
        errors.append(msg)
        return ""
    return r.stdout


def collect_vdevs(vdev_dict, pool_name, acc):
    for vdev_name, vdev in vdev_dict.items():
        r_errs = vdev.get("read_errors", "0")
        w_errs = vdev.get("write_errors", "0")
        c_errs = vdev.get("checksum_errors", "0")
        acc["read"].append((pool_name, vdev_name, r_errs))
        acc["write"].append((pool_name, vdev_name, w_errs))
        acc["cksum"].append((pool_name, vdev_name, c_errs))
        if "slow_ios" in vdev:
            acc["slow"].append((pool_name, vdev_name, vdev["slow_ios"]))
        if "vdevs" in vdev:
            collect_vdevs(vdev["vdevs"], pool_name, acc)


def parse_scrub_age(end_time_str):
    # Strip timezone abbreviation before parsing:
    # "Wed Jul  1 05:56:09 AM MDT 2026" -> "Wed Jul  1 05:56:09 AM 2026"
    # Both end_time and datetime.now() are local time, so the age
    # delta is correct without timezone conversion.
    cleaned = re.sub(r"\s+[A-Z]{2,4}\s+(\d{4})$", r" \1", end_time_str).strip()
    try:
        dt = datetime.strptime(cleaned, "%a %b %d %I:%M:%S %p %Y")
        return (datetime.now() - dt).total_seconds()
    except ValueError:
        return None


def metric(name, labels, value):
    label_str = ",".join(f'{k}="{v}"' for k, v in labels.items())
    return f"{name}{{{label_str}}} {value}"


def main():
    fields = "name,health,size,free,allocated,fragmentation"
    list_out = run_tabular("list", "-H", "-p", "-o", fields)
    status_data = run_json("status", "-j")

    lines = []

    pools = {}
    for row in list_out.strip().splitlines():
        parts = row.split("\t")
        if len(parts) < 6:
            continue
        name, health, size, free, alloc, frag = parts[:6]
        frag_ratio = int(frag) / 100 if frag not in ("-", "") else 0
        pools[name] = {
            "health": health,
            "size": size,
            "free": free,
            "alloc": alloc,
            "frag": f"{frag_ratio:.4f}",
        }

    lines += [
        "# HELP zpool_health Pool health (1=ONLINE, 0=not ONLINE)",
        "# TYPE zpool_health gauge",
    ]
    for n, p in pools.items():
        lines.append(
            metric(
                "zpool_health",
                {"pool": n, "state": p["health"]},
                1 if p["health"] == "ONLINE" else 0,
            )
        )

    for mname, help_txt, field in [
        ("zpool_size_bytes", "Pool total size in bytes", "size"),
        ("zpool_free_bytes", "Pool free space in bytes", "free"),
        ("zpool_allocated_bytes", "Pool allocated space in bytes", "alloc"),
        (
            "zpool_fragmentation_ratio",
            "Pool fragmentation ratio (0-1)",
            "frag",
        ),
    ]:
        lines += [f"# HELP {mname} {help_txt}", f"# TYPE {mname} gauge"]
        for n, p in pools.items():
            lines.append(metric(mname, {"pool": n}, p[field]))

    if status_data:
        scrub_age_lines, scrub_err_lines = [], []
        acc = {"read": [], "write": [], "cksum": [], "slow": []}

        for pool_name, pool in status_data.get("pools", {}).items():
            scan = pool.get("scan_stats", {})
            if scan and scan.get("function") == "SCRUB":
                scrub_err_lines.append(
                    metric(
                        "zpool_scrub_errors",
                        {"pool": pool_name},
                        scan.get("errors", "0"),
                    )
                )
                if scan.get("state") == "FINISHED" and scan.get("end_time"):
                    age = parse_scrub_age(scan["end_time"])
                    if age is not None:
                        scrub_age_lines.append(
                            metric(
                                "zpool_scrub_age_seconds",
                                {"pool": pool_name},
                                f"{age:.0f}",
                            )
                        )
            collect_vdevs(pool.get("vdevs", {}), pool_name, acc)

        if scrub_age_lines:
            lines += [
                "# HELP zpool_scrub_age_seconds"
                " Seconds since last completed scrub",
                "# TYPE zpool_scrub_age_seconds gauge",
            ]
            lines += scrub_age_lines

        if scrub_err_lines:
            lines += [
                "# HELP zpool_scrub_errors"
                " Errors found in last completed scrub",
                "# TYPE zpool_scrub_errors gauge",
            ]
            lines += scrub_err_lines

        for key, mname, help_txt in [
            (
                "read",
                "zpool_vdev_read_errors",
                "Cumulative read errors on vdev since last zpool clear",
            ),
            (
                "write",
                "zpool_vdev_write_errors",
                "Cumulative write errors on vdev since last zpool clear",
            ),
            (
                "cksum",
                "zpool_vdev_checksum_errors",
                "Cumulative checksum errors on vdev since last zpool clear",
            ),
        ]:
            if acc[key]:
                lines += [
                    f"# HELP {mname} {help_txt}",
                    f"# TYPE {mname} gauge",
                ]
                for pn, vn, val in acc[key]:
                    lines.append(metric(mname, {"pool": pn, "vdev": vn}, val))

        if acc["slow"]:
            lines += [
                "# HELP zpool_vdev_slow_ios Slow I/O count on vdev",
                "# TYPE zpool_vdev_slow_ios gauge",
            ]
            for pn, vn, val in acc["slow"]:
                lines.append(
                    metric(
                        "zpool_vdev_slow_ios", {"pool": pn, "vdev": vn}, val
                    )
                )

    tmp = OUTPUT + ".tmp"
    with open(tmp, "w") as f:
        f.write("\n".join(lines) + "\n")
    os.rename(tmp, OUTPUT)

    if errors:
        sys.exit(1)


main()
