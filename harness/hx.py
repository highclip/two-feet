#!/usr/bin/env python3
"""hx: append-only event log for an agent hackathon harness. Python stdlib only.

Every agent, hook and human action appends one JSON line to events.jsonl.
The board (board.html via `hx serve`) and `hx status` are projections of that log.

The log lives at $HX_LOG, or <main repo>/.harness/events.jsonl. It resolves through
git's common dir, so builders in separate worktrees all write to the same file.
"""
import argparse
import datetime as dt
import fcntl
import hashlib
import http.server
import json
import os
import pathlib
import signal
import subprocess
import sys
import time

STATES = ["planned", "building", "gates", "review", "blocked", "merged"]
REQUIRED = {
    "run.start": ["name", "deadline"],
    "slice.planned": ["slice", "title"],
    "slice.state": ["slice", "to"],
    "gate.result": ["slice", "gate", "status"],
    "review.verdict": ["slice", "reviewer", "verdict"],
    "decision.request": ["id", "question", "options"],
    "decision.resolved": ["id", "choice"],
    "agent.usage": ["agent", "cost_usd"],
    "demo.check": ["status", "flow"],
    "spec.loaded": ["slices", "sha"],
    "decision.made": ["text"],
    "agent.error": ["agent", "detail"],
}
ENUMS = {
    ("slice.state", "to"): STATES,
    ("gate.result", "status"): ["pass", "fail"],
    ("review.verdict", "reviewer"): ["security", "design", "code"],
    ("review.verdict", "verdict"): ["approve", "changes"],
    ("demo.check", "status"): ["pass", "fail"],
}
STUCK_MIN = {"building": 20, "gates": 10, "review": 15, "blocked": 5}
ACTIVE = ("building", "gates", "review", "blocked")
CHOICE_MAX = 2000  # 300 cut a planner note off mid-word, and the builder couldn't recover the rest
# Generated or lock files overlap all the time and say nothing about two slices' approaches.
NOISE = ("_templ.go", "go.sum", "package-lock.json", ".gitkeep")
SPEC_TO_JSON = r"""
import json, sys, yaml
spec = yaml.safe_load(open(sys.argv[1]))
print(json.dumps([{
    "id": s["id"], "title": s.get("title"), "depends_on": s.get("depends_on") or [],
    "scope": s.get("scope"), "acceptance": [str(c) for c in s.get("acceptance") or []],
} for s in spec["slices"]]))
"""


def default_log() -> pathlib.Path:
    if os.environ.get("HX_LOG"):
        return pathlib.Path(os.environ["HX_LOG"])
    try:
        common = subprocess.run(
            ["git", "rev-parse", "--path-format=absolute", "--git-common-dir"],
            capture_output=True, text=True, check=True,
        ).stdout.strip()
        return pathlib.Path(common).parent / ".harness" / "events.jsonl"
    except Exception:
        return pathlib.Path(".harness/events.jsonl").resolve()


LOG = default_log()


def git(*args) -> str:
    return subprocess.run(["git", *args], capture_output=True, text=True).stdout


def repo_root() -> pathlib.Path:
    return pathlib.Path(git("rev-parse", "--path-format=absolute", "--git-common-dir").strip()).parent


def slice_branch(slice_id: str):
    refs = git("for-each-ref", "--format=%(refname:short)", f"refs/heads/slice/{slice_id}-*").split()
    return refs[0] if refs else None


def changed_files(ref: str) -> list:
    return [f for f in git("diff", "--name-only", f"main...{ref}").split("\n")
            if f and not f.endswith(NOISE)]

RUN = os.environ.get("HX_RUN", "hack")
AGENT = os.environ.get("HX_AGENT", "unknown")


def utcnow() -> str:
    return dt.datetime.now(dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def parse_ts(s: str) -> float:
    return dt.datetime.strptime(s, "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=dt.timezone.utc).timestamp()


def validate(ev: dict) -> None:
    t = ev.get("type")
    if t not in REQUIRED:
        raise SystemExit(f"hx: unknown event type {t!r}")
    missing = [k for k in REQUIRED[t] if ev.get(k) in (None, "", [])]
    if missing:
        raise SystemExit(f"hx: {t} is missing {', '.join(missing)}")
    for (et, key), allowed in ENUMS.items():
        if et == t and ev[key] not in allowed:
            raise SystemExit(f"hx: {t}.{key} must be one of {', '.join(allowed)}")


def read_events() -> list:
    if not LOG.exists():
        return []
    out = []
    for line in LOG.read_text().splitlines():
        if line.strip():
            try:
                out.append(json.loads(line))
            except json.JSONDecodeError:
                pass  # a torn line never blocks the board
    return out


def emit(fields: dict, fill=None) -> dict:
    """Append one event under an exclusive lock. `fill` sets fields that depend on the log, such as ids."""
    LOG.parent.mkdir(parents=True, exist_ok=True)
    with open(LOG, "a") as f:
        fcntl.flock(f, fcntl.LOCK_EX)
        try:
            if fill:
                fill(fields)
            ev = {"ts": utcnow(), "run": RUN, "agent": AGENT}
            ev.update({k: v for k, v in fields.items() if v is not None})
            validate(ev)
            f.write(json.dumps(ev, ensure_ascii=False, separators=(",", ":")) + "\n")
            f.flush()
        finally:
            fcntl.flock(f, fcntl.LOCK_UN)
    return ev


def reduce(events: list) -> dict:
    run, slices, decisions, spend = None, {}, {}, 0.0
    spec, choices, errors = None, [], {}

    def sl(e):
        return slices.setdefault(e["slice"], {"slice": e["slice"], "title": e["slice"], "state": "planned",
                                              "since": e["ts"], "owner": None, "last_fail": None,
                                              "gate_fail": None, "reviews": {}})

    for e in events:
        t = e.get("type")
        if t == "run.start":
            run = e
        elif t == "slice.planned":
            s = sl(e)
            s.update(title=e["title"], since=e["ts"])
        elif t == "slice.state":
            s = sl(e)
            s.update(state=e["to"], since=e["ts"])
            if e["to"] == "building" and e.get("agent") not in (None, "gates", "unknown"):
                s["owner"] = e["agent"]
            if e["to"] == "building" and e.get("agent") not in ("gates",):
                # New work is coming: earlier verdicts no longer describe the code. (A re-run of the
                # gates on unchanged code must not erase them; found in the dry run.)
                s["reviews"] = {}
            if e["to"] in ("review", "merged"):
                s["gate_fail"] = None
        elif t == "gate.result" and e["status"] == "fail":
            sl(e)["gate_fail"] = f'{e["gate"]}: {e.get("detail") or "failed"}'
        elif t == "review.verdict":
            sl(e)["reviews"][e["reviewer"]] = {"verdict": e["verdict"], "findings": e.get("findings") or [],
                                              "ts": e["ts"]}
        elif t == "decision.request":
            decisions[e["id"]] = {**e, "open": True}
        elif t == "decision.resolved" and e["id"] in decisions:
            decisions[e["id"]].update(open=False, choice=e["choice"])
        elif t == "agent.usage":
            spend += float(e["cost_usd"])
            errors.pop(e.get("agent"), None)  # the same agent ran fine since
        elif t == "spec.loaded":
            spec = e
        elif t == "decision.made":
            choices.append(e)
        elif t == "agent.error":
            errors[e["agent"]] = e
    for s in slices.values():
        changes = [f'{r} review: {(v["findings"] or ["changes requested"])[0]}'
                   for r, v in s["reviews"].items() if v["verdict"] == "changes"]
        s["last_fail"] = changes[0] if changes else s["gate_fail"]
    return {"run": run, "slices": slices, "decisions": decisions, "spend": round(spend, 2),
            "spec": spec, "choices": choices, "errors": errors}


# ── commands ──────────────────────────────────────────────────────────────

def cmd_run(a):
    deadline = dt.datetime.now(dt.timezone.utc) + dt.timedelta(hours=a.hours)
    emit({"type": "run.start", "name": a.name, "deadline": deadline.strftime("%Y-%m-%dT%H:%M:%SZ"),
          "budget_usd": a.budget})


def cmd_plan(a):
    emit({"type": "slice.planned", "slice": a.slice, "title": a.title, "spec": a.spec})


def cmd_state(a):
    emit({"type": "slice.state", "slice": a.slice, "to": a.to, "reason": a.reason, "sha": a.sha})


def cmd_gate(a):
    emit({"type": "gate.result", "slice": a.slice, "gate": a.gate, "status": a.status, "detail": a.detail})


def cmd_review(a):
    emit({"type": "review.verdict", "slice": a.slice, "reviewer": a.reviewer, "verdict": a.verdict,
          "findings": a.finding or None})


def wait_for(decision_id: str, timeout: int):
    end = time.time() + timeout
    while time.time() < end:
        for e in read_events():
            if e.get("type") == "decision.resolved" and e.get("id") == decision_id:
                return e["choice"]
        time.sleep(2)
    return None


def cmd_ask(a):
    prior = reduce(read_events())["slices"].get(a.slice, {}).get("state") if a.slice else None

    def next_id(fields):
        n = sum(1 for e in read_events() if e.get("type") == "decision.request") + 1
        fields["id"] = f"D{n}"

    blocking = not a.fyi
    # With --no-wait nobody is left to move the slice back when the answer comes, so record where it was.
    ev = emit({"type": "decision.request", "slice": a.slice, "question": a.question, "options": a.option,
               "blocking": blocking, "prior_state": prior if blocking and a.no_wait else None}, fill=next_id)
    if not blocking or a.no_wait:
        if blocking and a.slice:
            emit({"type": "slice.state", "slice": a.slice, "to": "blocked", "reason": ev["id"]})
        print(ev["id"])
        return
    if a.slice:
        emit({"type": "slice.state", "slice": a.slice, "to": "blocked", "reason": ev["id"]})
    choice = wait_for(ev["id"], a.timeout)
    if choice is None:
        print(f"hx: no answer to {ev['id']} yet. Run `hx wait {ev['id']}` to keep waiting.", file=sys.stderr)
        sys.exit(3)
    if a.slice and prior and prior != "blocked":
        emit({"type": "slice.state", "slice": a.slice, "to": prior, "reason": f"{ev['id']} answered"})
    print(choice)


def unblock_after_answer(decision_id: str):
    """Put a slice back once the last --no-wait question holding it is answered."""
    st = reduce(read_events())
    d = st["decisions"][decision_id]
    s = st["slices"].get(d.get("slice"))
    if not s or not d.get("prior_state") or s["state"] != "blocked":
        return
    if any(o["open"] and o.get("blocking", True) and o.get("slice") == d["slice"] for o in st["decisions"].values()):
        return
    prior = d["prior_state"] if d["prior_state"] != "blocked" else "planned"
    emit({"type": "slice.state", "slice": d["slice"], "to": prior, "reason": f"{decision_id} answered", "agent": "human"})


def cmd_wait(a):
    choice = wait_for(a.id, a.timeout)
    if choice is None:
        print(f"hx: no answer to {a.id} yet", file=sys.stderr)
        sys.exit(3)
    print(choice)


def cmd_decide(a):
    st = reduce(read_events())
    d = st["decisions"].get(a.id)
    if not d:
        raise SystemExit(f"hx: no decision {a.id}")
    if not d["open"]:
        raise SystemExit(f"hx: {a.id} was already answered: {d['choice']}")
    emit({"type": "decision.resolved", "id": a.id, "choice": a.choice, "agent": "human"})
    unblock_after_answer(a.id)


def cmd_decisions(a):
    st = reduce(read_events())
    rows = [d for d in st["decisions"].values()
            if not a.slice or d.get("slice") in (a.slice, None)]
    # Reviewers read this command and are meant not to see builders' reasoning, so choices are opt-in.
    made = [c for c in st["choices"] if not a.slice or c.get("slice") in (a.slice, None)] if a.choices else []
    if a.json:
        print(json.dumps(rows + made, indent=2))
        return
    if not rows and not made:
        print("No decisions" + (f" for {a.slice}" if a.slice else "") + ".")
        return
    for d in rows:
        answer = f'ANSWERED: {d["choice"]}' if not d["open"] else "OPEN (waiting for the human)"
        print(f'{d["id"]} [{d.get("slice") or "all slices"}] {d["question"]}\n    {answer}')
    for c in made:
        why = f' Why: {c["why"]}' if c.get("why") else ""
        print(f'AGENT CHOICE [{c.get("slice") or "all slices"}] {c["text"]}{why}')


def cmd_findings(a):
    s = reduce(read_events())["slices"].get(a.slice)
    if not s:
        raise SystemExit(f"hx: no slice {a.slice}")
    print(f'{a.slice} {s["title"]}: {s["state"]}')
    if s["gate_fail"]:
        print(f'  gates: {s["gate_fail"]}  (run harness/gates.sh </dev/null for the full report)')
    if not s["reviews"]:
        print("  no review verdicts yet")
    for r, v in s["reviews"].items():
        print(f'  {r}: {v["verdict"]}')
        for f in v["findings"]:
            print(f'    - {f}')


def cmd_spec(a):
    """Copy the slices into the log, so the board and replays show the spec as it was at that moment."""
    path = pathlib.Path(a.file)
    if not path.exists():
        raise SystemExit(f"hx: no spec at {path}")
    sha = hashlib.sha256(path.read_bytes()).hexdigest()[:12]
    last = reduce(read_events())["spec"]
    if last and last["sha"] == sha:
        print(f"Spec {sha} is already loaded.")
        return
    # hx itself stays stdlib only, so the YAML parsing runs in the venv from scripts/setup-tools.sh.
    python = repo_root() / ".venv" / "bin" / "python"
    if not python.exists():
        raise SystemExit(f"hx: missing {python}: run scripts/setup-tools.sh")
    out = subprocess.run([str(python), "-c", SPEC_TO_JSON, str(path)], capture_output=True, text=True)
    if out.returncode:
        raise SystemExit(f"hx: can't read {path}: {out.stderr.strip()}")
    slices = json.loads(out.stdout)
    emit({"type": "spec.loaded", "path": str(path), "sha": sha, "slices": slices})
    print(f"Spec {sha}: {len(slices)} slices.")


def record_choice(slice_id, text, why=None, sha=None, source="note", agent=None):
    fields = {"type": "decision.made", "slice": slice_id, "text": text[:CHOICE_MAX], "why": why[:CHOICE_MAX] if why else None,
              "sha": sha, "source": source}
    if agent:
        fields["agent"] = agent
    emit(fields)


def cmd_note(a):
    record_choice(a.slice, a.text, a.why)


def cmd_harvest(a):
    """Turn `Decision:` lines in a slice's commit messages into events. gates.sh calls this on every run."""
    seen = {(e.get("slice"), e["text"]) for e in reduce(read_events())["choices"]}
    log = git("log", "--reverse", f"{a.base}..HEAD", "--format=%h%x00%B%x01")
    for commit in log.split("\x01"):
        sha, _, body = commit.strip().partition("\x00")
        for line in body.splitlines():
            if not line.startswith("Decision:"):
                continue
            text, _, why = line[len("Decision:"):].strip().partition(" Why: ")
            if text and (a.slice, text[:CHOICE_MAX]) not in seen:
                record_choice(a.slice, text, why or None, sha=sha, source="commit", agent=f"builder-{a.slice}")
                seen.add((a.slice, text[:CHOICE_MAX]))


def overlaps(slice_id: str, st: dict) -> dict:
    """Files this slice shares with each other active slice, from what's committed on their branches."""
    mine_ref = slice_branch(slice_id)
    if not mine_ref:
        return {}
    mine, out = set(changed_files(mine_ref)), {}
    for other in st["slices"].values():
        if other["slice"] == slice_id or other["state"] not in ACTIVE:
            continue
        ref = slice_branch(other["slice"])
        shared = sorted(mine & set(changed_files(ref))) if ref else []
        if shared:
            out[other["slice"]] = shared
    return out


def cmd_peers(a):
    st = reduce(read_events())
    shared = overlaps(a.slice, st)
    if a.overlap:
        for other, files in sorted(shared.items()):
            print(f'{other}: {", ".join(files)}')
        return
    ref = slice_branch(a.slice)
    behind = int(git("rev-list", "--count", f"{ref}..main").strip() or 0) if ref else 0
    if behind:
        print(f"main has {behind} commit(s) you don't. Bring them in: git merge main\n")
    others = [s for s in st["slices"].values() if s["slice"] != a.slice and s["state"] in ACTIVE]
    if not others:
        print("No other slice is active.")
    for s in sorted(others, key=lambda s: s["slice"]):
        print(f'{s["slice"]} {s["title"]} · {s["state"]} · {s["owner"] or "no owner"}')
        peer_ref = slice_branch(s["slice"])
        files = changed_files(peer_ref) if peer_ref else []
        print(f'  changed ({len(files)}): {", ".join(files[:15]) or "nothing committed yet"}')
        if s["slice"] in shared:
            print(f'  ALSO CHANGED BY YOU: {", ".join(shared[s["slice"]])}')
        for c in st["choices"]:
            if c.get("slice") == s["slice"]:
                print(f'  chose: {c["text"]}')
    print("\nThis shows committed work only.")


def cmd_error(a):
    emit({"type": "agent.error", "slice": a.slice, "detail": a.detail[:300]})


def cmd_state_of(a):
    print(reduce(read_events())["slices"].get(a.slice, {}).get("state", ""))


def cmd_usage(a):
    emit({"type": "agent.usage", "cost_usd": a.cost, "tokens_in": a.tokens_in, "tokens_out": a.tokens_out})


def cmd_demo(a):
    emit({"type": "demo.check", "status": a.status, "flow": a.flow, "detail": a.detail})


def cmd_emit(a):
    ev = json.loads(a.json)
    print(json.dumps(emit(ev)))


def cmd_path(_):
    print(LOG)


def cmd_status(a):
    st = reduce(read_events())
    if a.json:
        print(json.dumps(st, indent=2))
        return
    now = time.time()
    run = st["run"]
    if run:
        left = max(0, parse_ts(run["deadline"]) - now)
        budget = f' of ${run["budget_usd"]:.0f}' if run.get("budget_usd") else ""
        print(f'{run["name"]} · {int(left // 3600)}:{int(left % 3600 // 60):02d} left · ${st["spend"]:.2f}{budget}')
    order = {s: i for i, s in enumerate(["blocked", "gates", "building", "review", "planned", "merged"])}
    for s in sorted(st["slices"].values(), key=lambda s: (order[s["state"]], s["slice"])):
        mins = int((now - parse_ts(s["since"])) // 60)
        stuck = " STUCK" if mins > STUCK_MIN.get(s["state"], 10**9) else ""
        note = f'  ! {s["last_fail"]}' if s["last_fail"] else ""
        print(f'{s["slice"]:<4} {s["state"]:<9} {mins:>4}m{stuck:<6} {s["owner"] or "":<11} {s["title"]}{note}')
    done_d = [d for d in st["decisions"].values() if not d["open"]]
    if done_d:
        print("\nDecided (these override the spec):")
        for d in done_d[-5:]:
            print(f'  {d["id"]} [{d.get("slice") or "-"}] {d["question"]}  → {d["choice"]}')
    if st["choices"]:
        print("\nAgent choices (these don't override the spec):")
        for c in st["choices"][-5:]:
            print(f'  [{c.get("slice") or "-"}] {c["text"]}')
    if st["errors"]:
        print("\nAgent failures:")
        for who, e in st["errors"].items():
            print(f'  {who}: {e["detail"]}')
    open_d = [d for d in st["decisions"].values() if d["open"]]
    if open_d:
        print("\nWaiting on you:")
        for d in open_d:
            tag = "blocking" if d.get("blocking", True) else "fyi"
            print(f'  {d["id"]} [{d.get("slice") or "-"} · {tag}] {d["question"]}  → {" | ".join(d["options"])}')


def cmd_serve(a):
    board = pathlib.Path(__file__).with_name("board.html")
    allowed_origins = {f"http://127.0.0.1:{a.port}", f"http://localhost:{a.port}"}

    class Handler(http.server.BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def send(self, code, body, ctype="text/plain; charset=utf-8"):
            b = body if isinstance(body, bytes) else body.encode()
            self.send_response(code)
            self.send_header("Content-Type", ctype)
            self.send_header("Cache-Control", "no-store")
            self.send_header("Content-Length", str(len(b)))
            self.end_headers()
            self.wfile.write(b)

        def do_GET(self):
            path = self.path.split("?")[0]
            if path in ("/", "/board.html"):
                self.send(200, board.read_bytes(), "text/html; charset=utf-8")
            elif path == "/events.jsonl":
                self.send(200, LOG.read_bytes() if LOG.exists() else b"", "application/x-ndjson")
            else:
                self.send(404, "not found")

        def do_POST(self):
            # The board may only answer open decisions. JSON content-type plus an Origin check
            # keeps other pages in your browser from posting to this port.
            if self.path != "/events":
                return self.send(404, "not found")
            if self.headers.get("Content-Type", "").split(";")[0] != "application/json":
                return self.send(415, "expected application/json")
            origin = self.headers.get("Origin")
            if origin and origin not in allowed_origins:
                return self.send(403, "cross-origin request refused")
            n = int(self.headers.get("Content-Length") or 0)
            if n > 8192:
                return self.send(413, "too large")
            try:
                body = json.loads(self.rfile.read(n))
            except Exception:
                return self.send(400, "bad json")
            if body.get("type") != "decision.resolved":
                return self.send(403, "the board can only answer decisions")
            d = reduce(read_events())["decisions"].get(str(body.get("id")))
            if not d or not d["open"]:
                return self.send(409, "that decision is not open")
            if str(body.get("choice")) not in d["options"]:
                return self.send(400, "choice is not one of the options")
            ev = emit({"type": "decision.resolved", "id": d["id"], "choice": str(body["choice"]), "agent": "human"})
            unblock_after_answer(d["id"])
            self.send(200, json.dumps(ev), "application/json")

    srv = http.server.ThreadingHTTPServer(("127.0.0.1", a.port), Handler)
    print(f"Board → http://127.0.0.1:{a.port}\nLog   → {LOG}")
    try:
        srv.serve_forever()
    except KeyboardInterrupt:
        pass


def main():
    global AGENT
    # `hx status | head` should just stop, not print a BrokenPipe traceback.
    signal.signal(signal.SIGPIPE, signal.SIG_DFL)
    p = argparse.ArgumentParser(prog="hx", description="Harness event log")
    p.add_argument("--agent", help="Who is writing (default $HX_AGENT)")
    sub = p.add_subparsers(dest="cmd", required=True)

    s = sub.add_parser("run", help="Start a run");  s.set_defaults(fn=cmd_run)
    s.add_argument("--name", required=True); s.add_argument("--hours", type=float, default=4)
    s.add_argument("--budget", type=float, help="USD")

    s = sub.add_parser("plan", help="Add a slice"); s.set_defaults(fn=cmd_plan)
    s.add_argument("slice"); s.add_argument("title"); s.add_argument("--spec")

    s = sub.add_parser("state", help="Move a slice"); s.set_defaults(fn=cmd_state)
    s.add_argument("slice"); s.add_argument("to", choices=STATES)
    s.add_argument("--reason"); s.add_argument("--sha")

    s = sub.add_parser("gate", help="Record a gate result"); s.set_defaults(fn=cmd_gate)
    s.add_argument("slice"); s.add_argument("gate"); s.add_argument("status", choices=["pass", "fail"])
    s.add_argument("--detail")

    s = sub.add_parser("review", help="Record a review verdict"); s.set_defaults(fn=cmd_review)
    s.add_argument("slice"); s.add_argument("reviewer", choices=["security", "design", "code"])
    s.add_argument("verdict", choices=["approve", "changes"]); s.add_argument("--finding", action="append")

    s = sub.add_parser("ask", help="Escalate a question to the human; prints the answer")
    s.set_defaults(fn=cmd_ask)
    s.add_argument("question"); s.add_argument("--option", action="append", required=True)
    s.add_argument("--slice"); s.add_argument("--fyi", action="store_true", help="Not blocking; don't wait")
    s.add_argument("--no-wait", action="store_true", help="Blocking, but return the id immediately")
    s.add_argument("--timeout", type=int, default=540, help="Seconds (keep under your tool's timeout)")

    s = sub.add_parser("wait", help="Wait for an answer"); s.set_defaults(fn=cmd_wait)
    s.add_argument("id"); s.add_argument("--timeout", type=int, default=540)

    s = sub.add_parser("decide", help="Answer a decision"); s.set_defaults(fn=cmd_decide)
    s.add_argument("id"); s.add_argument("choice")

    s = sub.add_parser("findings", help="Review verdicts and the last gate failure for a slice")
    s.set_defaults(fn=cmd_findings)
    s.add_argument("slice")

    s = sub.add_parser("decisions", help="List decisions and answers (answers override the spec)")
    s.set_defaults(fn=cmd_decisions)
    s.add_argument("--slice", help="Only this slice's decisions, plus ones for all slices")
    s.add_argument("--choices", action="store_true", help="Also list choices agents made on their own")
    s.add_argument("--json", action="store_true")

    s = sub.add_parser("spec", help="Copy the spec's slices into the log for the board")
    s.set_defaults(fn=cmd_spec)
    s.add_argument("--file", default="spec/spec.yaml")

    s = sub.add_parser("note", help="Record a choice an agent made on its own; doesn't override the spec")
    s.set_defaults(fn=cmd_note)
    s.add_argument("text"); s.add_argument("--slice"); s.add_argument("--why")

    s = sub.add_parser("harvest", help="Record Decision: lines from a slice's commits (gates.sh runs this)")
    s.set_defaults(fn=cmd_harvest)
    s.add_argument("slice"); s.add_argument("--base", default="main")

    s = sub.add_parser("peers", help="What other active slices are changing and have chosen")
    s.set_defaults(fn=cmd_peers)
    s.add_argument("slice"); s.add_argument("--overlap", action="store_true", help="Only the shared files")

    s = sub.add_parser("error", help="Record that an agent failed")
    s.set_defaults(fn=cmd_error)
    s.add_argument("--detail", required=True); s.add_argument("--slice")

    s = sub.add_parser("state-of", help="Print a slice's state")
    s.set_defaults(fn=cmd_state_of)
    s.add_argument("slice")

    s = sub.add_parser("usage", help="Record spend"); s.set_defaults(fn=cmd_usage)
    s.add_argument("--cost", type=float, required=True)
    s.add_argument("--tokens-in", type=int); s.add_argument("--tokens-out", type=int)

    s = sub.add_parser("demo", help="Record a demo-path check"); s.set_defaults(fn=cmd_demo)
    s.add_argument("status", choices=["pass", "fail"]); s.add_argument("--flow", required=True)
    s.add_argument("--detail")

    s = sub.add_parser("emit", help="Append a raw JSON event"); s.set_defaults(fn=cmd_emit)
    s.add_argument("json")

    s = sub.add_parser("status", help="Print the board"); s.set_defaults(fn=cmd_status)
    s.add_argument("--json", action="store_true")

    s = sub.add_parser("serve", help="Serve the live board"); s.set_defaults(fn=cmd_serve)
    s.add_argument("--port", type=int, default=4777)

    s = sub.add_parser("path", help="Print the log path"); s.set_defaults(fn=cmd_path)

    a = p.parse_args()
    if a.agent:
        AGENT = a.agent
    a.fn(a)


if __name__ == "__main__":
    main()
