#!/usr/bin/env bash
# check-sandbox.sh — verify the demo sandbox keeps its promises before recording:
#
#   * every item lu-cleaner reports lies inside the sandbox (nothing from the real machine);
#   * the synthetic data looks as the tapes expect (worktree statuses, categories);
#   * the sandbox profile denies reading the real home and writing outside the sandbox;
#   * the no-op stand-ins are used instead of the real xcrun, brew, docker...;
#   * a real `clean --yes --smart` inside the sandbox removes the merged worktrees and
#     keeps the dirty one, the unpushed one and every branch.
#
#   demo/check-sandbox.sh          # uses LU_DEMO_BIN, else bin/lu-cleaner, else demo/bin/lu-cleaner
#
# Exits non-zero on the first failed check. The sandbox is created in a temporary folder
# and removed afterwards.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
fails=0
ok() { printf '  ok    %s\n' "$*"; }
ko() { printf '  FAIL  %s\n' "$*"; fails=$((fails + 1)); }
check() { # check <description> <command...>
	local what=$1
	shift
	if "$@" >/dev/null 2>&1; then ok "$what"; else ko "$what"; fi
}

tmp="$(mktemp -d /tmp/lu-cleaner-demo-check.XXXXXX)"
tmp="$(cd "$tmp" && pwd -P)"
root="$tmp/sandbox"
trap 'chmod -R u+w "$tmp" 2>/dev/null || true; rm -rf "$tmp"' EXIT

"$here/setup-sandbox.sh" "$root" >/dev/null
# shellcheck disable=SC1091
source "$root/env.sh"
H="$root/home"
real_home="$(dscl . -read "/Users/$(id -un)" NFSHomeDirectory | awk '{print $2}')"
real_home="$(cd "$real_home" && pwd -P)"

echo "sandbox: $root ($(du -sm "$root" | cut -f1) MB)"

echo "scan"
lu-cleaner scan --json >"$tmp/scan.json" 2>"$tmp/scan.err" || ko "scan exits 0"
python3 - "$tmp/scan.json" "$root" <<'EOF' || fails=$((fails + $?))
import json, sys
d = json.load(open(sys.argv[1]))
root = sys.argv[2] + "/"
# Grouped items (sessions by project...) have no single path.
bad = [f"{i['category']}: {i['name']} ({i['path']})" for i in d["items"]
       if i.get("path") and not i["path"].startswith(root)]
cats = {i["category"] for i in d["items"]}
wts = {i["name"]: i for i in d["items"] if i["category"] == "worktrees"}
checks = [
    ("every item is inside the sandbox" + ("" if not bad else ": " + "; ".join(bad[:5])), not bad),
    ("no scanner error: %s" % (d["errors"] or "none"), not d["errors"]),
    ("categories worktrees, artifacts, xcode, android, ai, js", {"worktrees", "artifacts", "xcode", "android", "ai", "js"} <= cats),
    ("4 worktrees", len(wts) == 4),
    *[(f"{n} recommended (warn: {wts.get(n, {}).get('warn')})", wts.get(n, {}).get("recommended"))
      for n in ("shop-app · codex/fix-cart-total", "my-app · dev/lisbon")],
    ("dirty cursor worktree is caution", wts.get("shop-app · feat/dark-mode", {}).get("risk") == "caution"),
    ("unpushed claude worktree is not recommended", not wts.get("my-app · worktree-onboarding", {}).get("recommended", True)),
]
failed = 0
for what, good in checks:
    print(("  ok    " if good else "  FAIL  ") + what)
    failed += not good
sys.exit(failed)
EOF

echo "sandbox profile"
sb() { /usr/bin/sandbox-exec -f "$root/sandbox.sb" /bin/sh -c "$1"; }
check "cannot write outside the sandbox" bash -c "! /usr/bin/sandbox-exec -f '$root/sandbox.sb' /usr/bin/touch '$tmp/outside' && [ ! -e '$tmp/outside' ]"
check "can write inside the sandbox" sb "touch '$root/tmp/inside' && rm '$root/tmp/inside'"
check "cannot read the real home" bash -c "! /usr/bin/sandbox-exec -f '$root/sandbox.sb' /bin/ls '$real_home'"
check "cannot list /Library" bash -c "! /usr/bin/sandbox-exec -f '$root/sandbox.sb' /bin/ls /Library"
check "cannot run the real xcrun" bash -c "! /usr/bin/sandbox-exec -f '$root/sandbox.sb' /usr/bin/xcrun --version"
check "can run /bin/ps (in-use guards)" sb "/bin/ps -axo comm= | grep -q ."
check "stand-ins were called instead of real tools" grep -q '^xcrun simctl list' "$root/fake-calls.log"

echo "busy sampler (demo/record.sh)"
LU_DEMO_BUSY_RE=launchd lu-cleaner scan --summary -c js >/dev/null 2>&1 || true
check "logs guarded processes seen while lu-cleaner runs" grep -qx launchd "$root/busy.log"
check "stops with lu-cleaner" test ! -e "$root/busy.pid"
rm -f "$root/busy.log"
lu-cleaner scan --summary -c js >/dev/null 2>&1 || true
check "off without LU_DEMO_BUSY_RE" test ! -e "$root/busy.log"

echo "clean --yes --smart (inside the sandbox)"
lu-cleaner clean --yes --smart >"$tmp/clean.out" 2>&1 || ko "clean exits 0 ($(tail -3 "$tmp/clean.out" | tr '\n' ' '))"
check "merged codex worktree removed" test ! -e "$H/.codex/worktrees/7f3a/shop-app"
check "merged conductor worktree removed" test ! -e "$H/conductor/workspaces/my-app/lisbon"
check "dirty cursor worktree kept" test -f "$H/.cursor/worktrees/shop-app/dark-mode/src/components/ThemeToggle.tsx"
check "unpushed claude worktree kept" test -d "$H/code/my-app/.claude/worktrees/onboarding"
check "branches of removed worktrees kept" bash -c "git -C '$H/code/shop-app' rev-parse -q --verify refs/heads/codex/fix-cart-total && git -C '$H/code/my-app' rev-parse -q --verify refs/heads/dev/lisbon"
check "stale node_modules removed" test ! -e "$H/code/shop-app/node_modules"
check "active project's node_modules kept" test -d "$H/code/my-app/node_modules"
check "sources kept" test -f "$H/code/shop-app/src/App.tsx"

echo
if [[ $fails -gt 0 ]]; then
	echo "$fails check(s) failed"
	exit 1
fi
echo "all checks passed"
