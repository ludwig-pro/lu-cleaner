#!/usr/bin/env python3
"""Opt-in, bounded macOS scan/cohabitation benchmark; never scans the real home.

Build an audited baseline in a temporary git archive and the current binary,
then pass --baseline /tmp/.../lu-cleaner --current ./bin/lu-cleaner.
Creation is excluded from measurements. JSON output records all raw samples.
"""
import argparse
import ctypes
import fcntl
import hashlib
import json
import multiprocessing as mp
import os
from pathlib import Path
import platform
import re
import shutil
import signal
import subprocess
import tempfile
import threading
import time


def fixture(base, packages):
    roots = []
    payload = b"bounded benchmark fixture\n" * 8
    for n in range(12):
        root = base / "roots" / f"r{n:02d}"
        root.mkdir(parents=True)
        (root / "package.json").write_text('{"private":true}')
        modules = root / "node_modules"
        for package in range(packages):
            pkg = modules / f"pkg{package:04d}"
            pkg.mkdir(parents=True)
            for f in range(4):
                (pkg / f"file{f}.js").write_bytes(payload)
        os.link(modules / "pkg0000/file0.js", modules / "hardlink.js")
        deep = modules / "deep"
        for _ in range(32):
            deep /= "d"
        deep.mkdir(parents=True)
        (deep / "leaf").write_bytes(payload)
        roots.append(str(root))
    flat = Path(roots[0]) / "node_modules" / "wide"
    flat.mkdir()
    for i in range(2048):
        (flat / f"f{i:04d}").write_bytes(payload)
    clone = subprocess.run(["/bin/cp", "-c", str(flat / "f0000"), str(flat / "clone")], capture_output=True)
    return roots, clone.returncode == 0


class RUsage(ctypes.Structure):
    # Public Darwin SDK sys/resource.h rusage_info_v2.
    _fields_ = [("uuid", ctypes.c_uint8 * 16)] + [(name, ctypes.c_uint64) for name in (
        "user", "system", "idle", "interrupt", "pageins", "wired", "resident", "footprint",
        "start", "exit", "child_user", "child_system", "child_idle", "child_interrupt", "child_pageins",
        "child_elapsed", "read", "written")]


def monitor(pid, stop, peak):
    try:
        lib = ctypes.CDLL("/usr/lib/libproc.dylib")
        while not stop.wait(0.005):
            usage = RUsage()
            if lib.proc_pid_rusage(pid, 2, ctypes.byref(usage)) == 0:
                peak["disk_read_bytes_sampled"] = max(peak.get("disk_read_bytes_sampled", 0), usage.read)
                peak["disk_written_bytes_sampled"] = max(peak.get("disk_written_bytes_sampled", 0), usage.written)
    except OSError:
        pass


def args_for(binary, mode, roots):
    args = [binary, "artifacts", *roots, "--json", "--min-size", "0", "--verbose"]
    if mode != "baseline":
        args += ["--scan-mode", mode]
    return args


def measure(args, env, cancel_after_first_walk=False):
    # Temporary files avoid full pipes when waiting with wait4 (which also
    # returns this child's CPU/RSS counters). No CPU quotas are set for it.
    with tempfile.TemporaryFile() as out, tempfile.TemporaryFile() as err:
        start = time.monotonic()
        p = subprocess.Popen(args, env=env, stdin=subprocess.DEVNULL, stdout=out, stderr=err)
        stop, peak = threading.Event(), {}
        watcher = threading.Thread(target=monitor, args=(p.pid, stop, peak), daemon=True)
        watcher.start()
        cancel_at, reaped = None, None
        if cancel_after_first_walk:
            # A timer after process start could cancel only CLI preparation.
            # Observe a completed walk while other roots are still running.
            deadline = start + 60
            while time.monotonic() < deadline:
                if b"[trace] walk " in os.pread(err.fileno(), os.fstat(err.fileno()).st_size, 0):
                    cancel_at = time.monotonic()
                    try: os.kill(p.pid, signal.SIGINT)
                    except ProcessLookupError: pass
                    break
                done_pid, status, usage = os.wait4(p.pid, os.WNOHANG)
                if done_pid:
                    reaped = (done_pid, status, usage)
                    break
                time.sleep(0.001)
            if cancel_at is None and reaped is None:
                p.kill(); os.wait4(p.pid, 0)
                raise RuntimeError("no completed walk observed within sixty seconds")
        _, status, usage = reaped or os.wait4(p.pid, 0)
        end = time.monotonic()
        p.returncode = os.waitstatus_to_exitcode(status)
        stop.set(); watcher.join()
        out.seek(0); err.seek(0)
        stdout, stderr = out.read().decode(), err.read().decode()
        stats = re.search(r"scan resources: (.*)", stderr)
        result = dict(wall_s=end-start, user_s=usage.ru_utime, system_s=usage.ru_stime,
                      max_rss_bytes=usage.ru_maxrss, in_blocks=usage.ru_inblock,
                      out_blocks=usage.ru_oublock, exit_code=p.returncode, **peak)
        if stats:
            result["scan_resources"] = stats.group(1)
        if cancel_after_first_walk:
            result["cancellation_exercised"] = p.returncode == 130
            result["cancel_phase"] = "after_first_completed_walk" if cancel_at else "scan_completed_before_signal"
            result["cancel_join_s"] = end-cancel_at if cancel_at and p.returncode == 130 else None
        elif p.returncode != 0:
            raise RuntimeError(f"{args}: {stderr}")
        report = json.loads(stdout) if stdout.strip() else None
        return result, report


def normalized(report):
    # Preserve paths, sizes, reclaimable bytes, risks and selection decisions.
    items = []
    for item in report["items"]:
        item = dict(item)
        item.pop("age_days", None)
        items.append(item)
    return sorted(items, key=lambda x: x["id"]), report["totals"], report["errors"]


def cpu_work(count):
    payload = b"x" * (256 << 10)
    for _ in range(count):
        hashlib.sha256(payload).digest()


def witness(kind, path, cpu_count, io_count, io_sync, io_no_cache):
    start = time.monotonic()
    if kind == "cpu":
        workers = [mp.Process(target=cpu_work, args=(cpu_count,)) for _ in range(2)]
        for p in workers: p.start()
        for p in workers: p.join()
        if any(p.exitcode != 0 for p in workers):
            raise RuntimeError("CPU witness failed")
    else:
        # Fixed work on a separate 32 MiB file. Explicit descriptor/cache and
        # sync settings are recorded; CPU and I/O run separately.
        block = b"w" * (1 << 20)
        with open(path, "r+b", buffering=0) as f:
            if io_no_cache:
                fcntl.fcntl(f.fileno(), 48, 1) # Darwin SDK F_NOCACHE
            sync = os.fsync if io_sync == "fsync" else lambda fd: fcntl.fcntl(fd, 51) # F_FULLFSYNC
            for i in range(io_count):
                f.seek((i % 32) << 20); f.read(1 << 20)
                f.seek(((i+7) % 32) << 20); f.write(block)
                if i % 4 == 0: sync(f.fileno())
            sync(f.fileno())
    return time.monotonic()-start


def cohabit(args, env, kind, path, cpu_count, io_count, io_sync, io_no_cache):
    done = threading.Event()
    runs = []
    failures = []
    def scanning():
        try:
            while not done.is_set():
                e = dict(env, LU_NO_CACHE="1")
                with tempfile.TemporaryFile() as out, tempfile.TemporaryFile() as err:
                    p = subprocess.Popen(args, env=e, stdin=subprocess.DEVNULL, stdout=out, stderr=err)
                    while p.poll() is None and not done.wait(0.01): pass
                    requested_stop = p.poll() is None
                    if requested_stop:
                        try: p.send_signal(signal.SIGINT)
                        except ProcessLookupError: pass
                    try:
                        p.wait(timeout=5)
                    except subprocess.TimeoutExpired:
                        p.kill(); p.wait()
                        raise RuntimeError("scan did not join within five seconds")
                    runs.append(p.returncode)
                    # An invocation may have just been spawned when the fixed
                    # witness ends. SIGINT before Go installs the CLI handler
                    # is a normal signal exit (-2 in Popen, 130 in the shell).
                    # Only accept it when this harness requested that stop.
                    if p.returncode not in (0, 130) and not (requested_stop and p.returncode == -signal.SIGINT):
                        err.seek(0)
                        raise RuntimeError(f"scan exited {p.returncode}: {err.read().decode()}")
        except Exception as error:
            failures.append(error)
    thread = threading.Thread(target=scanning)
    thread.start()
    try:
        elapsed = witness(kind, path, cpu_count, io_count, io_sync, io_no_cache)
    finally:
        done.set(); thread.join()
    if failures:
        raise failures[0]
    return dict(witness_s=elapsed, scan_invocations=len(runs), scan_exit_codes=runs)


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--baseline", required=True)
    ap.add_argument("--current", required=True)
    ap.add_argument("--output", required=True)
    ap.add_argument("--samples", type=int, default=3)
    ap.add_argument("--packages", type=int, default=32)
    ap.add_argument("--cpu-iterations", type=int, default=16000)
    ap.add_argument("--io-iterations", type=int, default=512)
    ap.add_argument("--witness-kind", choices=("both", "cpu", "io"), default="both")
    ap.add_argument("--io-sync", choices=("fullfsync", "fsync"), default="fullfsync")
    ap.add_argument("--io-no-cache", action="store_true", help="use macOS F_NOCACHE for the I/O witness")
    ap.add_argument("--witness-only", action="store_true", help="repeat cohabitation only, with standalone controls before and after each trial")
    opts = ap.parse_args()
    if platform.system() != "Darwin":
        ap.error("this benchmark uses the macOS process and storage APIs")
    if opts.samples < 1 or not 1 <= opts.packages <= 256 or opts.cpu_iterations < 1 or opts.io_iterations < 1:
        ap.error("samples/iterations must be positive and packages between 1 and 256")
    result = {"machine": {"platform": platform.platform(), "cpu_count": os.cpu_count(),
                         "inherited_nice": os.getpriority(os.PRIO_PROCESS, 0),
                         "inherited_darwin_background": ctypes.CDLL(None, use_errno=True).getpriority(4, 0),
                         "go": subprocess.check_output(["go", "version"], text=True).strip()},
              "parameters": vars(opts), "modes": {}, "witness": {},
              "scan_environment": {"GOMAXPROCS": "unset", "LU_WALKERS": "unset", "GOFLAGS": "unset", "cache": "isolated HOME; empty then reused"},
              "notes": ["RSS is wait4 peak; disk bytes are sampled proc_pid_rusage lower bounds, excluding descendants.",
                        "Filesystem page cache is not flushed; cold means an empty lu-cleaner size cache.",
                        "Cohabitation repeats uncached scans for the duration of fixed two-worker CPU or bounded I/O work."]}
    result["binaries"] = {
        key: {"path": str(Path(path).resolve()), "sha256": hashlib.sha256(Path(path).read_bytes()).hexdigest()}
        for key, path in (("baseline", opts.baseline), ("current", opts.current))
    }
    for key in ("hw.model", "hw.memsize", "hw.physicalcpu", "hw.logicalcpu"):
        result["machine"][key] = subprocess.check_output(["sysctl", "-n", key], text=True).strip()
    result["complete"] = False
    def checkpoint():
        Path(opts.output).write_text(json.dumps(result,indent=2)+"\n")
    checkpoint()
    with tempfile.TemporaryDirectory(prefix="lu-cleaner-bench-") as temporary:
        base = Path(temporary)
        roots, clones = fixture(base, opts.packages)
        print("temporary fixtures:", base, flush=True)
        result["apfs_clone_created"] = clones
        env = dict(os.environ)
        for key in ("GOMAXPROCS", "GOFLAGS", "LU_WALKERS", "LU_NO_BULK", "LU_NO_CLONES", "LU_NO_CACHE", "XDG_CACHE_HOME", "LU_CLEANER_CONFIG"):
            env.pop(key, None)
        env.update(HOME=str(base/"home"), XDG_CONFIG_HOME=str(base/"config"), LU_TRACE="1")
        Path(env["HOME"]).mkdir()
        config = Path(env["XDG_CONFIG_HOME"])/"lu-cleaner/config.toml"
        config.parent.mkdir(parents=True); config.write_text('roots = []\nworktree_roots = []\nmin_size = "0"\n')
        io_path = base/"witness.bin"
        io_path.write_bytes(b"w" * (32 << 20))
        all_reports = []
        for mode in (() if opts.witness_only else ("baseline", "fast", "eco")):
            binary = str(Path(opts.baseline if mode=="baseline" else opts.current).resolve())
            args = args_for(binary, mode, roots)
            records = {"cold": [], "warm": [], "cancel": []}
            for _ in range(opts.samples):
                shutil.rmtree(Path(env["HOME"])/"Library/Caches/lu-cleaner", ignore_errors=True)
                for cache in ("cold", "warm"):
                    record, report = measure(args, env)
                    records[cache].append(record); all_reports.append(normalized(report))
                    print(mode, cache, round(record["wall_s"],3), "s", flush=True)
            record,_ = measure(args,dict(env,LU_NO_CACHE="1",LU_TRACE_MS="1"),cancel_after_first_walk=True)
            records["cancel"].append(record)
            result["modes"][mode] = records
            checkpoint()
        result["results_identical"] = all(report == all_reports[0] for report in all_reports) if all_reports else None
        checkpoint()
        kinds = ("cpu", "io") if opts.witness_kind == "both" else (opts.witness_kind,)
        for kind in kinds:
            samples = {"alone": [], "baseline": [], "fast": [], "eco": []}
            result["witness"][kind] = samples
            for trial in range(opts.samples):
                # Interleave each concurrent trial with a fresh standalone
                # control to reduce drift from unrelated machine activity.
                modes = ("baseline", "fast", "eco")
                # Rotate order and bracket each trial. Retain controls on the
                # record itself so ambient storage variation stays visible.
                modes = modes[trial % 3:] + modes[:trial % 3]
                for mode in modes:
                    before = witness(kind,io_path,opts.cpu_iterations,opts.io_iterations,opts.io_sync,opts.io_no_cache)
                    samples["alone"].append(before)
                    binary=str(Path(opts.baseline if mode=="baseline" else opts.current).resolve())
                    r=cohabit(args_for(binary,mode,roots),env,kind,io_path,opts.cpu_iterations,opts.io_iterations,opts.io_sync,opts.io_no_cache)
                    r["alone_before_s"] = before
                    r["alone_after_s"] = witness(kind,io_path,opts.cpu_iterations,opts.io_iterations,opts.io_sync,opts.io_no_cache)
                    samples[mode].append(r)
                    checkpoint()
                    print("cohabitation",kind,mode,round(r["witness_s"],3),"s",flush=True)
    result["complete"] = True
    checkpoint()
    if result["results_identical"] is False:
        raise SystemExit("scan results differ; inspect raw JSON before accepting")


if __name__ == "__main__":
    main()
