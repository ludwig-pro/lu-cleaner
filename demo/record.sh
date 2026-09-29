#!/usr/bin/env bash
# record.sh — re-record the animated demos of lu-cleaner with VHS.
#
#   demo/record.sh                  # every tape of demo/tapes (dashboard, worktrees, scan)
#   demo/record.sh dashboard scan   # only these
#
# Each tape runs against a fresh SYNTHETIC sandbox (demo/setup-sandbox.sh):
# fake home, fake projects and worktrees, no-op stand-ins for xcrun / brew /
# docker..., and a macOS sandbox profile that forbids reading your home and
# writing outside the sandbox. The dashboard and worktrees tapes really clean
# inside it; the sandbox is deleted afterwards.
#
# Outputs: site/public/demos/<tape>.gif, .mp4 and .jpg (the video poster: the
# frame at the "# poster: <seconds>" comment of the tape), served by the
# documentation site at /lu-cleaner/demos/ and referenced by README.md.
#
# Environment:
#   LU_DEMO_ROOT     sandbox folder (default /tmp/lu-cleaner-demo; it shows in
#                    the details panel of the recordings)
#   LU_DEMO_BIN      use this lu-cleaner binary instead of building one
#   LU_DEMO_BUSY_RE  processes that make lu-cleaner hold items back while they
#                    run (in-use guards: "xcodebuild is running"), so a
#                    recording made meanwhile would differ; default: the
#                    "# busy: <regex>" comment of the tape, else
#                    Xcode|xcodebuild|codex|Codex|ChatGPT; empty: no check.
#                    record.sh waits until none has run for 5 s, and the
#                    sandbox's lu-cleaner function logs the ones seen while it
#                    runs: the tape is then recorded again.
#   LU_DEMO_TRIES    attempts per tape (default 3)
#   LU_DEMO_MAX_KB   size warning threshold per file (default 3072)
set -euo pipefail

die() { echo "record: $*" >&2; exit 1; }
log() { echo "record: $*" >&2; }

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repo="$(cd "$here/.." && pwd -P)"
cd "$repo"

[[ "$(uname -s)" == Darwin ]] || die "macOS only"
for t in vhs ttyd ffmpeg; do
	command -v "$t" >/dev/null || die "$t not found: brew install vhs (it pulls ttyd and ffmpeg)"
done

sandbox="${LU_DEMO_ROOT:-/tmp/lu-cleaner-demo}"
default_busy_re="Xcode|xcodebuild|codex|Codex|ChatGPT"
busy_re=""
tries="${LU_DEMO_TRIES:-3}"
max_kb="${LU_DEMO_MAX_KB:-3072}"
out_dir="site/public/demos"
mkdir -p "$out_dir"

tapes=("$@")
if [[ ${#tapes[@]} -eq 0 ]]; then
	tapes=(dashboard worktrees scan)
fi
for t in "${tapes[@]}"; do
	[[ -f "demo/tapes/$t.tape" ]] || die "no tape demo/tapes/$t.tape"
done

# ------------------------------------------------------------------ binary
if [[ -n "${LU_DEMO_BIN:-}" ]]; then
	[[ -x "$LU_DEMO_BIN" ]] || die "LU_DEMO_BIN=$LU_DEMO_BIN is not executable"
else
	log "building demo/bin/lu-cleaner"
	GOTOOLCHAIN=local go build -trimpath -ldflags "-s -w -X main.version=demo" -o demo/bin/lu-cleaner ./cmd/lu-cleaner
	export LU_DEMO_BIN="$repo/demo/bin/lu-cleaner"
fi

# ------------------------------------------------------------------ helpers
cleanup() {
	# Only ever remove a folder that setup-sandbox.sh created (marker file).
	if [[ -f "$sandbox/.lu-cleaner-demo-sandbox" && ! -L "$sandbox" ]]; then
		chmod -R u+w "$sandbox" 2>/dev/null || true
		rm -rf "$sandbox"
	fi
}
trap cleanup EXIT
trap 'exit 130' INT TERM

busy() { /bin/ps -axo comm= | awk -F/ '{print $NF}' | grep -qxE "$busy_re"; }

# wait_quiet: wait (up to 10 min) until none of the guarded processes has run for 5 s.
wait_quiet() {
	local quiet=0 waited=0
	while [[ $quiet -lt 5 ]]; do
		if busy; then
			quiet=0
			[[ $waited -eq 0 ]] && log "waiting for $(/bin/ps -axo comm= | awk -F/ '{print $NF}' | grep -xE "$busy_re" | sort -u | tr '\n' ' ')to finish…"
		else
			quiet=$((quiet + 1))
		fi
		sleep 1
		waited=$((waited + 1))
		[[ $waited -lt 600 ]] || die "guarded processes still running after 10 min (LU_DEMO_BUSY_RE='' to ignore)"
	done
}

size_kb() { echo $(($(stat -f %z "$1") / 1024)); }

# ------------------------------------------------------------------ record
# tape_comment <tape> <key>: the value of a "# <key>: <value>" comment line.
tape_comment() { sed -n "s/^# $2: *\([^ ]*\).*\$/\1/p" "demo/tapes/$1.tape" | head -n 1; }

for t in "${tapes[@]}"; do
	if [[ -n "${LU_DEMO_BUSY_RE+set}" ]]; then
		busy_re="$LU_DEMO_BUSY_RE"
	else
		busy_re="$(tape_comment "$t" busy)"
		busy_re="${busy_re:-$default_busy_re}"
	fi
	attempt=1
	while :; do
		[[ -n "$busy_re" ]] && wait_quiet
		dir="$("$here/setup-sandbox.sh" "$sandbox")"
		log "recording $t (attempt $attempt)"
		# The sandbox's lu-cleaner function logs the guarded processes it sees
		# running meanwhile into $dir/busy.log (LU_DEMO_BUSY_RE).
		LU_DEMO_ENV="$dir/env.sh" LU_DEMO_BUSY_RE="$busy_re" vhs -q "demo/tapes/$t.tape"
		# busy.log and fake-calls.log may not exist (LU_DEMO_BUSY_RE empty, nothing called).
		calls="$(sort -u "$dir/fake-calls.log" 2>/dev/null | tr '\n' ';' || true)"
		seen="$(sort -u "$dir/busy.log" 2>/dev/null | tr '\n' ' ' || true)"
		cleanup
		if [[ -n "$seen" ]]; then
			((attempt < tries)) || {
				log "WARNING: $t recorded while ${seen}ran; items may show 'is running'"
				break
			}
			log "${seen}ran during the recording of $t: recording again"
			attempt=$((attempt + 1))
			continue
		fi
		break
	done
	[[ -n "${LU_DEMO_VERBOSE:-}" ]] && log "no-op stand-ins called: $calls"
	poster_at="$(tape_comment "$t" poster)"
	poster_at="${poster_at%s}"
	if [[ -n "$poster_at" && -f "$out_dir/$t.mp4" ]]; then
		ffmpeg -v error -y -ss "$poster_at" -i "$out_dir/$t.mp4" -frames:v 1 -q:v 3 "$out_dir/$t.jpg"
	fi
	for f in "$out_dir/$t".{gif,mp4,jpg}; do
		[[ -f "$f" ]] || continue
		kb=$(size_kb "$f")
		log "$f: ${kb} KB"
		((kb <= max_kb)) || log "WARNING: $f is over ${max_kb} KB"
	done
done
