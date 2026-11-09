"""Exercise queue transitions without opening a real blockchain database."""

import importlib.util
import fcntl
import json
import os
from pathlib import Path
import signal
import shutil
import subprocess
import sys
import tempfile
import time
import unittest
from unittest import mock


SOURCE = Path(__file__).with_name("queue.py")
SPEC = importlib.util.spec_from_file_location("statesim_queue", SOURCE)
queue = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(queue)


class QueueTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.binary = self.root / "statesim"
        self.binary.write_text(f"#!{sys.executable}\n" + '''
import json, os, pathlib, sys, time
args = sys.argv
path = pathlib.Path(args[args.index('-datadir') + 1])
path.mkdir()
(path / 'pid').write_text(str(os.getpid()))
time.sleep(float(os.environ.get('WAIT', '0')))
mode = os.environ.get('MODE', 'complete')
if mode == 'error': sys.exit(2)
(path / 'simblocks.jsonl').write_text(json.dumps({'Type': mode, 'Head': 2}) + '\\n')
(path / 'checkpoint.json').write_text(json.dumps({'Head': {'Number': 2}}))
''')
        self.binary.chmod(0o755)
        (self.root / "input.rlp").touch()
        self.plan = self.root / "plan.json"
        self.config = {"version": 1, "lock_file": str(self.root / "queue.lock"),
                       "jobs": [self.job("first"), self.job("second")]}

    def job(self, name):
        return {"argv": [str(self.binary), "-datadir", str(self.root / name),
                         "-until", "2", "-blocks", str(self.root / "input.rlp")],
                "binary_sha256": queue.digest(self.binary), "cwd": str(self.root)}

    def invoke(self, expected=0, check=False):
        self.plan.write_text(json.dumps(self.config))
        cmd = [sys.executable, str(SOURCE), str(self.plan)]
        if check:
            cmd.append("--check")
        result = subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=10)
        self.assertEqual(result.returncode, expected, result.stdout.decode())
        return result

    def test_success_and_duplicate_invocation(self):
        self.invoke()
        stamp = (self.root / "first/pid").stat().st_mtime_ns
        self.invoke()
        self.assertEqual((self.root / "first/pid").stat().st_mtime_ns, stamp)
        self.assertEqual(json.loads(self.plan.with_suffix(".state.json").read_text())["status"], "complete")

    def test_nonzero_and_clean_early_stop_do_not_launch_successor(self):
        for mode in ("error", "stopped"):
            with self.subTest(mode=mode):
                self.config["jobs"] = [self.job(mode), self.job("never")]
                self.config["jobs"][0]["env"] = {"MODE": mode}
                self.plan = self.root / f"{mode}.json"
                self.invoke(1)
                self.assertFalse((self.root / "never").exists())

    def test_preflight_failure_does_not_launch_or_register(self):
        self.config["jobs"][0]["binary_sha256"] = "changed"
        self.invoke(1, check=True)
        self.assertFalse(self.plan.with_suffix(".state.json").exists())
        self.assertFalse((self.root / "first").exists())

    def test_existing_output_and_space_guard(self):
        (self.root / "first").mkdir()
        self.invoke(1, check=True)
        (self.root / "first").rmdir()
        self.config["jobs"][0]["min_free_gib"] = 10**12
        self.invoke(1, check=True)
        self.assertFalse((self.root / "first").exists())

    def test_completion_requires_checkpoint_and_full_final_record(self):
        self.invoke()
        directory = self.root / "first"
        (directory / "checkpoint.json").write_text('{"Head":{"Number":1}}')
        with self.assertRaises(ValueError):
            queue.completed(directory, 2)
        (directory / "checkpoint.json").write_text('{"Head":{"Number":2}}')
        with (directory / "simblocks.jsonl").open("a") as out:
            out.write('{"Type":')
        with self.assertRaises(ValueError):
            queue.completed(directory, 2)

    def test_checkpoint_clone_isolated_and_source_locked(self):
        source = self.root / "source"
        db = source / "chaindata"
        db.mkdir(parents=True)
        (db / "LOCK").touch()
        (db / "CURRENT").write_text("MANIFEST-000001\n")
        (db / "000001.ldb").write_bytes(b"immutable table")
        (source / "checkpoint.json").write_text(json.dumps({"StateScheme": "hash", "Head": {"Number": 1}}))
        (source / "replay.json").write_text('{"Replay":{"StateOnly":true}}')
        (source / "simblocks.jsonl").write_text('{"Type":"complete","Head":1}\n')
        job = self.job("clone")
        job["argv"].append("-resume")
        job["clone_from"] = {"datadir": str(source), "checkpoint_sha256": queue.digest(source / "checkpoint.json")}
        dest, _ = queue.preflight(job)
        with (db / "LOCK").open("rb") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            with self.assertRaises(BlockingIOError):
                queue.clone_checkpoint(job, dest)
            self.assertFalse(dest.exists())
        # Test files need not reside on a reflink-capable filesystem.
        with mock.patch.object(queue.subprocess, "run", side_effect=lambda argv, **kw: shutil.copytree(argv[-2], argv[-1])) as copy:
            queue.clone_checkpoint(job, dest)
            self.assertIn("--reflink=always", copy.call_args.args[0])
        (dest / "chaindata/CURRENT").write_text("changed")
        self.assertEqual((db / "CURRENT").read_text(), "MANIFEST-000001\n")
        self.assertEqual((dest / "simblocks.jsonl").read_text(), "")
        self.assertEqual(queue.digest(source / "checkpoint.json"), job["clone_from"]["checkpoint_sha256"])
        with self.assertRaises(ValueError):
            queue.preflight(job)  # Never overwrite a previous clone.
        job["argv"][job["argv"].index(str(dest))] = str(self.root / "other-clone")
        (source / "checkpoint.json").write_text("changed")
        with self.assertRaisesRegex(ValueError, "checkpoint changed"):
            queue.preflight(job)

    def test_bare_resume_rejected(self):
        job = self.job("resume")
        job["argv"].append("-resume")
        with self.assertRaisesRegex(ValueError, "clone_from"):
            queue.preflight(job)

    def test_careful_queue_preserves_distinct_results(self):
        scratch = self.root / "dbs/careful-run"
        scratch.mkdir(parents=True)
        for job in self.config["jobs"]:
            job["argv"] += ["-root", str(self.root), "-careful-run"]
            self.assertEqual(queue.database_path(job), scratch)
        self.invoke(check=True)
        self.assertTrue(scratch.exists())  # Preflight never clears DBs.
        self.invoke()
        for name in ("first", "second"):
            queue.completed(self.root / name, 2)

    def test_separate_database_clone_updates_paths_and_rejects_replaced_source(self):
        origin = self.root / "results/source"
        original_db = self.root / "dbs/source"
        origin.mkdir(parents=True)
        original_db.mkdir(parents=True)
        (original_db / "LOCK").touch()
        (original_db / "statesim-results").write_text(str(origin))
        (origin / "replay.json").write_text(json.dumps({"Replay": {"StateOnly": True}, "DatabasePath": str(original_db)}))
        (origin / "checkpoint.json").write_text('{"StateScheme":"hash","Head":{"Number":1}}')
        (origin / "simblocks.jsonl").write_text('{"Type":"complete","Head":1}\n')
        job = self.job("clone")
        copied_db = self.root / "dbs/clone"
        job["argv"] += ["-db-dir", str(copied_db), "-resume"]
        job["clone_from"] = {"datadir": str(origin), "checkpoint_sha256": queue.digest(origin / "checkpoint.json")}
        dest, _ = queue.preflight(job)
        with mock.patch.object(queue.subprocess, "run", side_effect=lambda argv, **kw: shutil.copytree(argv[-2], argv[-1])):
            queue.clone_checkpoint(job, dest)
        self.assertEqual(queue.source_database(dest), copied_db)
        self.assertEqual((copied_db / "statesim-results").read_text(), str(dest))
        self.assertEqual(queue.source_database(origin), original_db)
        (original_db / "statesim-results").write_text(str(self.root / "new-results"))
        with self.assertRaisesRegex(ValueError, "reused"):
            queue.source_database(origin)

    def test_waits_for_exact_predecessor_then_launches(self):
        predecessor = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(0.25)"])
        captured = queue.identity(predecessor.pid)
        folder = self.root / "previous"
        folder.mkdir()
        (folder / "simblocks.jsonl").write_text('{"Type":"complete","Head":2}\n')
        (folder / "checkpoint.json").write_text('{"Head":{"Number":2}}')
        (folder / "exit").write_text("0\n")
        self.config["after"] = {"process": captured, "datadir": str(folder),
                                "target": 2, "exit_file": str(folder / "exit")}
        self.plan.write_text(json.dumps(self.config))
        start = time.monotonic()
        try:
            queue.run(self.plan, poll=0.01)
        finally:
            predecessor.wait()
        self.assertGreaterEqual(time.monotonic() - start, 0.2)

    def test_missing_predecessor_exit_stops_queue(self):
        self.config["after"] = {"process": {"pid": os.getpid(), "start_ticks": "old", "boot_id": "old"},
                                "datadir": str(self.root), "target": 2,
                                "exit_file": str(self.root / "missing-exit")}
        self.invoke(1)
        self.assertFalse((self.root / "first").exists())

    def test_lock_survives_runner_death(self):
        self.config["jobs"] = [self.job("long")]
        self.config["jobs"][0]["env"] = {"WAIT": "30"}
        self.plan.write_text(json.dumps(self.config))
        runner = subprocess.Popen([sys.executable, str(SOURCE), str(self.plan)],
                                  stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        child_pid = None
        try:
            deadline = time.monotonic() + 5
            while not (self.root / "long/pid").exists():
                self.assertLess(time.monotonic(), deadline)
                time.sleep(0.01)
            child_pid = int((self.root / "long/pid").read_text())
            self.invoke(1)  # A second runner cannot enter while the first lives.
            runner.kill()
            runner.wait()
            self.plan = self.root / "another.json"
            self.config["jobs"] = [self.job("never")]
            self.invoke(1)  # An orphaned simulator still owns the queue lock.
            self.assertFalse((self.root / "never").exists())
        finally:
            if runner.poll() is None:
                runner.kill()
                runner.wait()
            if child_pid:
                os.kill(child_pid, signal.SIGTERM)


if __name__ == "__main__":
    unittest.main()
