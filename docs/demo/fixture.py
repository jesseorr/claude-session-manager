#!/usr/bin/env python3
"""Build a synthetic ~/.claude so cs can be screenshotted without real work in it."""
import json as _json, os, shutil, subprocess, sys, time


# Claude Code writes compact JSON; the scanner looks for `"key":"` with no space.
def dumps(o):
    return _json.dumps(o, separators=(",", ":"))


class json:
    dumps = staticmethod(dumps)
from pathlib import Path

HOME = Path(sys.argv[1])
shutil.rmtree(HOME, ignore_errors=True)
(HOME / ".claude" / "projects").mkdir(parents=True)
(HOME / ".claude" / "sessions").mkdir(parents=True)

NOW = time.time()
H = 3600


def encode(p):
    return str(p).replace("/", "-")


def patch(path, plus, minus):
    lines = ["+new line"] * plus + ["-old line"] * minus
    return {"type": "user", "toolUseResult": {"filePath": path,
            "structuredPatch": [{"lines": lines}]}}


def session(sid, cwd, branch, title, prompts, edits, start_ago, end_ago, live_pid=None):
    cwd = str(HOME / cwd)
    os.makedirs(cwd, exist_ok=True)
    d = HOME / ".claude" / "projects" / encode(cwd)
    d.mkdir(parents=True, exist_ok=True)
    f = d / f"{sid}.jsonl"

    rows = [{"type": "user", "cwd": cwd, "gitBranch": branch,
             "timestamp": time.strftime("%Y-%m-%dT%H:%M:%S.000Z",
                                        time.gmtime(NOW - start_ago))}]
    for p in prompts:
        rows.append({"type": "last-prompt", "lastPrompt": p, "sessionId": sid})
    rows.append({"type": "ai-title", "aiTitle": title, "sessionId": sid})
    for path, plus, minus in edits:
        rows.append(patch(str(HOME / path), plus, minus))

    f.write_text("".join(json.dumps(r) + "\n" for r in rows))
    # Pad the file so the reported size looks like a session of this length.
    with f.open("a") as fh:
        fh.write(json.dumps({"type": "attachment", "pad": "x" * 400 * len(prompts)}) + "\n")
    os.utime(f, (NOW - end_ago, NOW - end_ago))

    if live_pid:
        stat = Path(f"/proc/{live_pid}/stat").read_text()
        procstart = stat.rsplit(")", 1)[1].split()[19]
        rec = {"pid": live_pid, "sessionId": sid, "cwd": cwd, "kind": "interactive",
               "procStart": procstart, "status": "idle"}
        (HOME / ".claude" / "sessions" / f"{live_pid}.json").write_text(json.dumps(rec))


# Two long-lived processes stand in for sessions that are still open.
live = [subprocess.Popen(["sleep", "900"], stdout=subprocess.DEVNULL,
                         stderr=subprocess.DEVNULL, stdin=subprocess.DEVNULL,
                         start_new_session=True) for _ in range(2)]

session("a1b2c3d4-0000-4000-8000-000000000001", "work/api-gateway", "main",
        "Trace the retry loop in the upload worker",
        ["the 502s only happen on the multipart path, not the streaming one",
         "yes, proceed"] * 9,
        [("work/api-gateway/queue/retry.go", 185, 42),
         ("work/api-gateway/queue/worker.go", 133, 0),
         ("work/api-gateway/queue/backoff.go", 69, 15),
         ("work/api-gateway/internal/log/fields.go", 22, 4),
         ("work/api-gateway/README.md", 8, 1)],
        start_ago=21 * H, end_ago=40 * 60, live_pid=live[0].pid)

session("a1b2c3d4-0000-4000-8000-000000000002", "work/billing", "",
        "Port the billing tests to table driven",
        ["split the fixture into per-currency cases",
         "the rounding assertion is wrong for JPY, it has no minor unit"] * 6,
        [("work/billing/invoice_test.go", 240, 310),
         ("work/billing/rounding.go", 31, 12)],
        start_ago=27 * H, end_ago=95 * 60)

session("a1b2c3d4-0000-4000-8000-000000000003", "work/search-svc", "feat/pagination",
        "Add cursor pagination to the search endpoint",
        ["use an opaque cursor, not an offset, the index shifts under us"] * 4,
        [("work/search-svc/handler.go", 96, 21),
         ("work/search-svc/cursor.go", 74, 0)],
        start_ago=5 * H, end_ago=3 * H, live_pid=live[1].pid)

session("a1b2c3d4-0000-4000-8000-000000000004", "work/edge-fn", "main",
        "Investigate slow cold starts",
        ["p99 is 1.8s on a warm region, which is not cold start at all"] * 3,
        [("work/edge-fn/bundle.ts", 14, 58)],
        start_ago=30 * H, end_ago=26 * H)

session("a1b2c3d4-0000-4000-8000-000000000005", "notes", "",
        "Draft the on-call handover runbook",
        ["keep it to one page, the point is that it gets read at 3am"] * 2,
        [("notes/oncall.md", 120, 6)],
        start_ago=31 * H, end_ago=29 * H)

print(" ".join(str(p.pid) for p in live))
