#!/usr/bin/env python3
"""Compare three binaries on one bounded fixture, without scanning the real home.

Runs the pre-profiles binary, released eco, and local eco/fast sequentially.
Each trial rotates the order and starts with a separate empty size cache,
then reuses that cache. The OS page cache and other applications are not
controlled. No witness workload runs during these latency measurements.
"""
import argparse
import ctypes
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import signal
import subprocess
import tempfile
import threading
import time

from bench_scan import args_for, fixture, monitor, normalized


def measure(args, env, timeout):
    with tempfile.TemporaryFile() as out, tempfile.TemporaryFile() as err:
        start = time.monotonic()
        load_before = os.getloadavg()
        process = subprocess.Popen(args, env=env, stdin=subprocess.DEVNULL, stdout=out, stderr=err)
        stop, peak = threading.Event(), {}
        watcher = threading.Thread(target=monitor, args=(process.pid, stop, peak), daemon=True)
        watcher.start()
        cancel_at, forced, reaped = None, False, False
        try:
            while True:
                pid, status, usage = os.wait4(process.pid, os.WNOHANG)
                if pid:
                    reaped = True
                    break
                now = time.monotonic()
                if cancel_at is None and now - start >= timeout:
                    try:
                        os.kill(process.pid, signal.SIGINT)
                    except ProcessLookupError:
                        pass
                    cancel_at = now
                elif cancel_at is not None and not forced and now - cancel_at >= 5:
                    try:
                        os.kill(process.pid, signal.SIGKILL)
                    except ProcessLookupError:
                        pass
                    forced = True
                time.sleep(0.002)
            end = time.monotonic()
            process.returncode = os.waitstatus_to_exitcode(status)
        finally:
            if not reaped:
                process.kill()
                process.wait()
            stop.set()
            watcher.join()
        out.seek(0)
        err.seek(0)
        stdout, stderr = out.read().decode(), err.read().decode()
        record = dict(wall_s=end-start, user_s=usage.ru_utime, system_s=usage.ru_stime,
                      max_rss_bytes=usage.ru_maxrss, in_blocks=usage.ru_inblock,
                      out_blocks=usage.ru_oublock, exit_code=process.returncode,
                      timed_out=cancel_at is not None, forced_stop=forced,
                      load_average_before=load_before, load_average_after=os.getloadavg(), **peak)
        if cancel_at is not None:
            record["cancel_join_s"] = end-cancel_at
        # The fixture contains no personal paths. Keep cache/provider diagnostics
        # to distinguish a reused cache from an actual cache hit.
        record["diagnostics"] = [line for line in stderr.splitlines()
                                 if re.search(r"scan (limits|resources):|provider .* (done|failed)|\[trace\] cache|warning:", line)]
        report = json.loads(stdout) if process.returncode == 0 and stdout.strip() else None
        if process.returncode != 0:
            record["stderr_tail"] = stderr.splitlines()[-10:]
        return record, report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--before", required=True, help="binary before scan profiles were introduced")
    parser.add_argument("--released", required=True, help="released binary with the original eco profile")
    parser.add_argument("--current", required=True, help="local corrected binary")
    parser.add_argument("--output", required=True)
    parser.add_argument("--samples", type=int, default=3)
    parser.add_argument("--packages", type=int, default=128)
    parser.add_argument("--timeout", type=float, default=120, help="maximum seconds per scan before SIGINT")
    opts = parser.parse_args()
    if opts.samples < 1 or not 1 <= opts.packages <= 256 or opts.timeout <= 0:
        parser.error("samples and timeout must be positive; packages must be between 1 and 256")
    binaries = {key: str(Path(getattr(opts, key)).resolve()) for key in ("before", "released", "current")}
    profiles = [("before", "before", "baseline"), ("released_eco", "released", "eco"),
                ("current_eco", "current", "eco"), ("current_fast", "current", "fast")]
    result = {"parameters": vars(opts), "machine": {"platform": platform.platform(),
              "cpu_count": os.cpu_count(), "inherited_nice": os.getpriority(os.PRIO_PROCESS, 0),
              "inherited_darwin_background": ctypes.CDLL(None).getpriority(4, 0)},
              "binaries": {key: {"path": path, "sha256": hashlib.sha256(Path(path).read_bytes()).hexdigest()}
                           for key, path in binaries.items()},
              "notes": ["Same immutable artifact roots for every version; no general home scan.",
                        "Cold means empty application size cache; the OS page cache is not flushed.",
                        "Warm means a reused cache, not necessarily a cache hit; see diagnostics.",
                        "CPU is cumulative user + system time; 100% average means one logical core.",
                        "Disk bytes are sampled lower bounds for the scanner process, excluding descendants.",
                        "Other applications are not controlled; load averages and trial order are recorded."],
              "scan_environment": {"GOMAXPROCS": "unset", "LU_WALKERS": "unset", "GOFLAGS": "unset",
                                   "HOME": "isolated fixture", "config": "isolated", "LU_TRACE": "1"},
              "profiles": {name: {"cold": [], "warm": []} for name, _, _ in profiles},
              "order": [], "complete": False}
    for key in ("hw.model", "hw.memsize", "hw.physicalcpu", "hw.logicalcpu"):
        result["machine"][key] = subprocess.check_output(["sysctl", "-n", key], text=True).strip()

    def save():
        Path(opts.output).write_text(json.dumps(result, indent=2)+"\n")

    save()
    fingerprints, failures = [], []
    with tempfile.TemporaryDirectory(prefix="lu-compare-versions-") as temporary:
        base = Path(temporary)
        roots, clones = fixture(base, opts.packages)
        counts = {"files": 0, "directories": 0}
        for _, dirs, files in os.walk(base / "roots"):
            counts["files"] += len(files)
            counts["directories"] += len(dirs)
        result["fixture"] = {"roots": len(roots), "apfs_clone_created": clones, **counts}
        print("fixture", result["fixture"], flush=True)
        for trial in range(opts.samples):
            ordered = profiles[trial % len(profiles):] + profiles[:trial % len(profiles)]
            for name, binary, mode in ordered:
                env = dict(os.environ)
                for key in ("GOMAXPROCS", "GOFLAGS", "LU_WALKERS", "LU_NO_BULK", "LU_NO_CLONES", "LU_NO_CACHE",
                            "LU_TRACE_MS", "XDG_CACHE_HOME", "LU_CLEANER_CONFIG"):
                    env.pop(key, None)
                state = base / "state" / f"{trial}-{name}"
                env.update(HOME=str(state/"home"), XDG_CONFIG_HOME=str(state/"config"), LU_TRACE="1")
                Path(env["HOME"]).mkdir(parents=True)
                config = Path(env["XDG_CONFIG_HOME"]) / "lu-cleaner/config.toml"
                config.parent.mkdir(parents=True)
                config.write_text('roots = []\nworktree_roots = []\nmin_size = "0"\n')
                args = args_for(binaries[binary], mode, roots) + ["--dry-run"]
                for cache in ("cold", "warm"):
                    record, report = measure(args, env, opts.timeout)
                    record["trial"] = trial + 1
                    if report is not None:
                        fingerprint = hashlib.sha256(json.dumps(normalized(report), sort_keys=True).encode()).hexdigest()
                        fingerprints.append(fingerprint)
                        record["normalized_results_sha256"] = fingerprint
                        record["items"] = len(report["items"])
                        record["totals"] = report["totals"]
                    else:
                        failures.append([trial+1, name, cache])
                    result["profiles"][name][cache].append(record)
                    result["order"].append([trial+1, name, cache])
                    save()
                    print(trial+1, name, cache, round(record["wall_s"], 3), "s", "exit", record["exit_code"], flush=True)
                    if report is None:
                        break  # A partial cold scan is not a valid warm-cache trial.
    result.update(complete=True, failures=failures,
                  results_identical=bool(fingerprints) and not failures and len(set(fingerprints)) == 1)
    save()
    if not result["results_identical"]:
        raise SystemExit("comparison incomplete or results differ; inspect the raw output")


if __name__ == "__main__":
    main()
