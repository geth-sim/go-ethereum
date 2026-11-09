#!/usr/bin/env python3
"""Run isolated statesim experiments sequentially; stop on any incomplete run."""

import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import time
from datetime import datetime, timezone


def now():
    return datetime.now(timezone.utc).isoformat()


def identity(pid):
    """Distinguish exited/reused PIDs, including across host reboots (Linux)."""
    try:
        fields = Path(f"/proc/{pid}/stat").read_text().rsplit(")", 1)[1].split()
        if fields[0] == "Z":
            return None
        return {"pid": pid, "start_ticks": fields[19],
                "boot_id": Path("/proc/sys/kernel/random/boot_id").read_text().strip()}
    except FileNotFoundError:
        return None


def publish(path, value):
    temp = path.with_suffix(path.suffix + ".tmp")
    with temp.open("w") as out:
        json.dump(value, out, indent=2)
        out.write("\n")
        out.flush()
        os.fsync(out.fileno())
    os.replace(temp, path)
    fd = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def digest(path):
    h = hashlib.sha256()
    with path.open("rb") as src:
        for chunk in iter(lambda: src.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def flag(argv, name):
    values = []
    for index, arg in enumerate(argv[1:], 1):
        if arg in ("-" + name, "--" + name):
            values.append(argv[index + 1])
        elif arg.startswith(("-" + name + "=", "--" + name + "=")):
            values.append(arg.split("=", 1)[1])
    if len(values) != 1:
        raise ValueError(f"explicit, unique -{name} is required")
    return values[0]


def completed(datadir, target):
    """An exit code of zero can also mean a deliberate early stop."""
    path = datadir / "simblocks.jsonl"
    with path.open("rb") as src:
        size = src.seek(0, os.SEEK_END)
        src.seek(max(0, size - 65536))
        tail = src.read()
    if not tail.endswith(b"\n"):
        raise ValueError(f"incomplete result tail: {path}")
    last = json.loads(tail.splitlines()[-1])
    if last.get("Type") != "complete" or last.get("Head") != target:
        raise ValueError(f"missing completion at block {target}: {path}")
    checkpoint = json.loads((datadir / "checkpoint.json").read_text())
    if checkpoint["Head"]["Number"] != target:
        raise ValueError(f"checkpoint does not match block {target}: {datadir}")


def preflight(job):
    argv = job["argv"]
    binary = Path(argv[0])
    datadir = Path(flag(argv, "datadir"))
    blocks = Path(flag(argv, "blocks"))
    target = int(flag(argv, "until"))
    if not all(p.is_absolute() for p in (binary, datadir, blocks, Path(job["cwd"]))):
        raise ValueError("binary, datadir, blocks and cwd must be absolute paths")
    resume = [arg for arg in argv if arg.split("=", 1)[0] in ("-resume", "--resume")]
    source = job.get("clone_from")
    if bool(resume) != bool(source) or (resume and resume not in (["-resume"], ["-resume=true"])):
        raise ValueError("-resume requires clone_from; existing output DBs are never reused")
    if source:
        origin = Path(source["datadir"])
        if not origin.is_absolute() or not (origin / "chaindata").is_dir():
            raise ValueError("clone source must be an absolute checkpoint directory")
        if digest(origin / "checkpoint.json") != source["checkpoint_sha256"]:
            raise ValueError("source checkpoint changed")
        saved = json.loads((origin / "checkpoint.json").read_text())
        description = json.loads((origin / "replay.json").read_text())
        if saved.get("StateScheme") != "hash" or description.get("NonArchive") or not description["Replay"].get("StateOnly"):
            raise ValueError("checkpoint clones currently require archive, state-only hashdb")
        completed(origin, saved["Head"]["Number"])
        if target <= saved["Head"]["Number"]:
            raise ValueError("clone target must exceed the source head")
    if target <= 0 or not blocks.is_file() or not Path(job["cwd"]).is_dir():
        raise ValueError("invalid target, input file or working directory")
    if not os.access(binary, os.X_OK) or digest(binary) != job["binary_sha256"]:
        raise ValueError(f"executable missing or changed: {binary}")
    if datadir.exists():
        raise ValueError(f"refusing to reuse an existing output directory: {datadir}")
    # The existing parent also anchors the expected output filesystem.
    if not datadir.parent.is_dir():
        raise ValueError(f"output parent must already exist: {datadir.parent}")
    if shutil.disk_usage(datadir.parent).free < job.get("min_free_gib", 0) * 2**30:
        raise ValueError(f"insufficient free disk space: {datadir.parent}")
    return datadir, target


def clone_checkpoint(job, datadir):
    """Reflink a closed DB, preserving the source and sharing no writable files."""
    source = job["clone_from"]
    origin = Path(source["datadir"])
    with (origin / "chaindata/LOCK").open("rb") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        # Recheck after locking in case the source ran since preflight.
        preflight(job)
        datadir.mkdir()
        subprocess.run(["cp", "-a", "--reflink=always", "--no-target-directory",
                        str(origin / "chaindata"), str(datadir / "chaindata")], check=True)
        for name in ("replay.json", "checkpoint.json"):
            shutil.copy2(origin / name, datadir / name)
        # Earlier result rows remain in the source; this run records only its
        # resumed interval with a new RunID and freshly sampled engine counters.
        (datadir / "simblocks.jsonl").touch()
        publish(datadir / "clone.json", {**source, "created_at": now(), "method": "reflink"})


def run(plan_path, check=False, poll=30):
    raw = plan_path.read_bytes()
    plan = json.loads(raw)
    if plan.get("version") != 1 or not plan.get("jobs"):
        raise ValueError("expected version=1 and nonempty jobs")
    state_path = plan_path.with_suffix(".state.json")
    plan_hash = hashlib.sha256(raw).hexdigest()
    lock_path = Path(plan["lock_file"])
    if not lock_path.is_absolute():
        raise ValueError("lock_file must be an absolute path shared by all experiment queues")
    with lock_path.open("a+") as lock:
        # The child inherits this lock: killing the runner cannot allow another
        # queue to start a benchmark while its orphaned child is still running.
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        state = {"plan_sha256": plan_hash}
        if state_path.exists():
            state = json.loads(state_path.read_text())
            if state["plan_sha256"] != plan_hash:
                raise ValueError("plan changed since registration; use a new plan file")
            if state["status"] == "complete":
                print("Queue already completed; nothing to run.", flush=True)
                return
            if state["status"] != "waiting":
                raise ValueError("previous queue did not finish; inspect its state before retrying")

        def status(value, **fields):
            state.update(status=value, updated_at=now(), **fields)
            publish(state_path, state)
            print(f"{state['updated_at']} {value}: {fields}", flush=True)

        child = None
        try:
            outputs = []
            for job in plan["jobs"]:
                datadir, _ = preflight(job)
                outputs.append(datadir.resolve())
            if len(outputs) != len(set(outputs)):
                raise ValueError("jobs must have distinct output directories")
            after = plan.get("after")
            if after and any(p == Path(after["datadir"]).resolve() for p in outputs):
                raise ValueError("output must not overwrite the predecessor")
            if check:
                print("Preflight passed. No experiment started.", flush=True)
                return
            if after:
                status("waiting", predecessor=after)
                heartbeat = 0
                while identity(after["process"]["pid"]) == after["process"]:
                    if time.monotonic() >= heartbeat:
                        print(f"{now()} waiting for predecessor PID {after['process']['pid']}", flush=True)
                        heartbeat = time.monotonic() + 600
                    time.sleep(poll)
                if Path(after["exit_file"]).read_text().strip() != "0":
                    raise ValueError("predecessor failed; successor will not start")
                completed(Path(after["datadir"]), after["target"])
            for index, job in enumerate(plan["jobs"]):
                datadir, target = preflight(job)
                status("starting", job_index=index, argv=job["argv"])
                if job.get("clone_from"):
                    status("cloning", source=job["clone_from"]["datadir"])
                    clone_checkpoint(job, datadir)
                child = subprocess.Popen(job["argv"], cwd=job["cwd"],
                                         env={**os.environ, **job.get("env", {})},
                                         pass_fds=(lock.fileno(),), start_new_session=True)
                status("running", child=identity(child.pid), child_pid=child.pid)
                code = child.wait()
                child = None
                status("checking", exit_code=code)
                if code != 0:
                    raise ValueError(f"job {index} exited with code {code}")
                completed(datadir, target)
                status("job_complete", head=target)
            status("complete")
        except BaseException as error:
            if child is not None and child.poll() is None:
                child.send_signal(signal.SIGTERM)
                child.wait()  # statesim finishes its batch and closes its DB.
            if not check:
                status("cancelled" if isinstance(error, KeyboardInterrupt) else "failed",
                       error=str(error) or type(error).__name__)
            raise


def interrupted(*_):
    raise KeyboardInterrupt


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("plan", type=Path)
    parser.add_argument("--check", action="store_true", help="check inputs without starting or waiting")
    args = parser.parse_args()
    signal.signal(signal.SIGTERM, interrupted)
    try:
        run(args.plan.resolve(), args.check)
        return 0
    except KeyboardInterrupt:
        return 130
    except Exception as error:
        print(f"Queue stopped: {error}", file=sys.stderr, flush=True)
        return 1


if __name__ == "__main__":
    sys.exit(main())
