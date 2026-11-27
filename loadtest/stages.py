"""Per-stage statistics from a k6 JSON output (k6 run --out json=raw.json).

Usage: python loadtest/stages.py loadtest/results/raw.json
"""
import json
import sys
from datetime import datetime

# Windows (seconds from the first sample) where the load of both scenarios is steady.
STAGES = [
    ("normal: api 10 VU, jobs 2 VU", 30, 60),
    ("3x: api 30 VU, jobs 2->6 VU", 90, 120),
    ("6x (stress): api 60 VU, jobs 6 VU", 150, 180),
    ("recovery: api 10 VU, jobs 2 VU", 210, 240),
]


def pct(xs, p):
    if not xs:
        return float("nan")
    xs = sorted(xs)
    return xs[min(len(xs) - 1, int(round(p / 100 * (len(xs) - 1))))]


points = []
for line in open(sys.argv[1], encoding="utf-8"):
    o = json.loads(line)
    if o.get("type") != "Point":
        continue
    d = o["data"]
    t = datetime.fromisoformat(d["time"].replace("Z", "+00:00")).timestamp()
    points.append((t, o["metric"], d["value"], d.get("tags") or {}))

t0 = min(p[0] for p in points)
print("| Tahap | API req/s | API p50 (ms) | API p95 (ms) | Error rate | Job selesai/s | Queue wait p50 / p95 (ms) | Job e2e p50 / p95 (ms) |")
print("|---|---|---|---|---|---|---|---|")
for name, a, b in STAGES:
    win = [p for p in points if a <= p[0] - t0 < b]
    dur = [v for _, m, v, tg in win if m == "http_req_duration" and tg.get("scenario") == "api"]
    failed = [v for _, m, v, tg in win if m == "http_req_failed"]
    jobs = [v for _, m, v, _ in win if m == "jobs_completed"]
    wait = [v for _, m, v, _ in win if m == "job_queue_wait_ms"]
    e2e = [v for _, m, v, _ in win if m == "job_e2e_ms"]
    secs = b - a
    err = sum(failed) / len(failed) * 100 if failed else 0
    print(f"| {name} | {len(dur) / secs:.1f} | {pct(dur, 50):.1f} | {pct(dur, 95):.1f} | {err:.2f}% | "
          f"{sum(jobs) / secs:.2f} | {pct(wait, 50):.0f} / {pct(wait, 95):.0f} | {pct(e2e, 50):.0f} / {pct(e2e, 95):.0f} |")
