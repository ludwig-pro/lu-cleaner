#!/usr/bin/env bash
# setup-sandbox.sh — build a SYNTHETIC home folder to record the lu-cleaner demos.
#
#   demo/setup-sandbox.sh [DIR]        # default DIR: /tmp/lu-cleaner-demo
#
# DIR must not exist, be empty, or be a previous sandbox (it holds the marker
# file .lu-cleaner-demo-sandbox): that one is removed and rebuilt. Paths show
# in the recordings (details panel), hence a short fixed default.
#
# Prints DIR on stdout. `source DIR/env.sh` then gives a `lu-cleaner` shell
# function that runs the binary inside the sandbox, never on your machine:
#
#   * HOME, TMPDIR and XDG_* point into DIR (env -i: nothing else is inherited,
#     so ANDROID_HOME, GOPATH, NVM_DIR... of the real session do not leak in);
#   * PATH holds only the demo binary, git, plutil, sqlite3 and NO-OP stand-ins
#     for xcrun, xcodebuild, xcode-select, docker, colima, container, brew,
#     pnpm, watchman, osascript, tmutil, mdfind, defaults, pgrep, open... (they
#     log their arguments to DIR/fake-calls.log and do nothing);
#   * a macOS sandbox profile (sandbox-exec) denies reading the real home, the
#     real per-user temp/cache folders and the system Xcode / simulator data,
#     denies running the real xcrun/brew/docker/tmutil..., and denies WRITING
#     anywhere outside DIR (terminals excepted). Even a bug in lu-cleaner
#     could not touch a real file.
#
# The content is fake: a few React Native / Next.js projects with
# node_modules, Pods, iOS/Android builds; AI agent worktrees made with real
# git (Codex: merged and idle, Conductor: merged and idle, Cursor: dirty,
# Claude Code: commits never pushed); package-manager caches, DerivedData,
# Gradle caches and AI tools data. Files are real (lu-cleaner measures
# allocated blocks, sparse files would show 0 B) but small: about 185 MB in
# total (LU_DEMO_SCALE=<percent> scales them, default 50). Remove the sandbox
# with `rm -rf DIR` (record.sh does it for you).
set -euo pipefail

die() { echo "setup-sandbox: $*" >&2; exit 1; }

[[ "$(uname -s)" == Darwin ]] || die "macOS only (lu-cleaner and sandbox-exec are macOS tools)"
[[ -x /usr/bin/sandbox-exec ]] || die "/usr/bin/sandbox-exec not found"

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repo="$(cd "$here/.." && pwd -P)"

# The real home, from the directory service (not $HOME, which may be overridden).
real_home="$(dscl . -read "/Users/$(id -un)" NFSHomeDirectory 2>/dev/null | awk '{print $2}')"
[[ -n "$real_home" && -d "$real_home" ]] || real_home="$(cd ~ && pwd -P)"
real_home="$(cd "$real_home" && pwd -P)"

# Binary to put in the sandbox: $LU_DEMO_BIN, else bin/lu-cleaner (make build), else
# demo/bin/lu-cleaner (built by record.sh).
bin_src="${LU_DEMO_BIN:-}"
if [[ -z "$bin_src" ]]; then
	for c in "$repo/bin/lu-cleaner" "$here/bin/lu-cleaner"; do
		[[ -x "$c" ]] && { bin_src="$c"; break; }
	done
fi
[[ -n "$bin_src" && -x "$bin_src" ]] || die "no lu-cleaner binary: run 'make build' or set LU_DEMO_BIN"

# ------------------------------------------------------------------ sandbox dir
marker=.lu-cleaner-demo-sandbox
root="${1:-/tmp/lu-cleaner-demo}"
[[ "$root" == /* ]] || root="$PWD/$root"
[[ -L "$root" ]] && die "$root is a symlink"
parent="$(cd "$(dirname "$root")" && pwd -P)" || die "no parent folder for $root"
root="$parent/$(basename "$root")" # resolved (/tmp -> /private/tmp): lu-cleaner reports real paths
case "$root/" in
"$real_home"/*) die "the sandbox must live outside your home ($real_home): its profile denies reading the real home" ;;
/Users/* | /System/* | /Library/* | /Applications/* | /usr/* | /bin/* | /sbin/* | /opt/* | /private/var/* | /private/etc/*)
	die "refusing sandbox dir $root" ;;
esac
depth=0
IFS=/ read -ra parts <<<"$root"
for x in "${parts[@]}"; do [[ -z "$x" || "$x" == . ]] || depth=$((depth + 1)); done
[[ $depth -ge 3 && "$root" != *"/.."* ]] || die "refusing sandbox dir $root (too close to /)"
if [[ -e "$root" ]]; then
	[[ -d "$root" && -O "$root" ]] || die "$root exists and is not a folder of yours"
	if [[ -f "$root/$marker" ]]; then
		chmod -R u+w "$root" 2>/dev/null || true
		rm -rf "$root"
	elif [[ -n "$(ls -A "$root")" ]]; then
		die "$root exists, is not empty and is not a demo sandbox"
	fi
fi
mkdir -p "$root"
: >"$root/$marker"

H="$root/home"
mkdir -p "$H" "$root/bin" "$root/tmp" "$root/remotes"
: >"$root/fake-calls.log"

now=$(date +%s)
DAY=86400

# stamp <age-in-days> <path...>: set the mtime (and atime) of every file below path.
stamp() {
	local days=$1
	shift
	local t
	t=$(date -r $((now - days * DAY)) +%Y%m%d%H%M.%S)
	find "$@" -exec touch -h -t "$t" {} +
}

# blob <file> <KiB>: a real file (allocated blocks) of that size, scaled by
# LU_DEMO_SCALE percent (default 50: the whole sandbox stays under 200 MB).
scale_pct=${LU_DEMO_SCALE:-50}
blob() {
	mkdir -p "$(dirname "$1")"
	head -c $(($2 * scale_pct / 100 * 1024)) /dev/zero >"$1"
}

# npm_pkgs <node_modules> name:KiB...: fake packages (package.json, a few sources, one bundle).
npm_pkgs() {
	local nm=$1
	shift
	local spec name kb d
	for spec in "$@"; do
		name=${spec%%:*}
		kb=${spec##*:}
		d="$nm/$name"
		mkdir -p "$d/lib" "$d/dist"
		printf '{\n  "name": "%s",\n  "version": "1.%d.0",\n  "main": "dist/index.js"\n}\n' "$name" $((RANDOM % 20)) >"$d/package.json"
		printf '# %s\n' "$name" >"$d/README.md"
		printf 'module.exports = require("./dist/index.js");\n' >"$d/index.js"
		printf 'export {};\n' >"$d/lib/index.d.ts"
		blob "$d/dist/index.js" "$kb"
	done
	mkdir -p "$nm/.bin"
	printf '{"lockfileVersion":3}\n' >"$nm/.package-lock.json"
}

rn_modules=(
	react:512 react-native:9216 react-dom:768 @babel/core:1024 @babel/runtime:896 @babel/preset-env:640
	metro:1536 metro-runtime:256 hermes-parser:1280 @react-native/metro-config:128
	@react-navigation/native:384 @react-navigation/native-stack:256 react-native-screens:1024
	react-native-safe-area-context:192 react-native-reanimated:3072 react-native-gesture-handler:1536
	react-native-svg:1280 expo:640 expo-modules-core:1024 expo-router:896 @expo/cli:2560
	typescript:4096 eslint:1536 prettier:1024 jest:1280 @testing-library/react-native:256
	lodash:768 date-fns:1792 zod:512 @tanstack/react-query:640 zustand:128 i18next:384
	@sentry/react-native:2048 react-native-mmkv:512 @shopify/flash-list:384 lottie-react-native:896
)

# rn_project <dir> <Name> <node_modules scale %>: a React Native (Expo prebuild) app, git-tracked sources only.
rn_project() {
	local p=$1 name=$2 scale=$3 spec mods=()
	mkdir -p "$p/src/screens" "$p/src/components" "$p/ios/$name.xcodeproj" "$p/ios/$name" "$p/android/app/src/main"
	cat >"$p/package.json" <<EOF
{
  "name": "$(basename "$p")",
  "version": "1.4.0",
  "private": true,
  "main": "expo-router/entry",
  "scripts": { "start": "expo start", "ios": "expo run:ios", "android": "expo run:android", "test": "jest" }
}
EOF
	printf '{ "expo": { "name": "%s", "slug": "%s" } }\n' "$name" "$(basename "$p")" >"$p/app.json"
	printf 'module.exports = { preset: "react-native" };\n' >"$p/jest.config.js"
	printf 'export default function App() { return null; }\n' >"$p/src/App.tsx"
	printf 'export function Home() { return null; }\n' >"$p/src/screens/Home.tsx"
	printf 'export function Button() { return null; }\n' >"$p/src/components/Button.tsx"
	cat >"$p/.gitignore" <<'EOF'
node_modules/
.expo/
ios/Pods/
ios/build/
android/.gradle/
android/build/
android/app/build/
.env
EOF
	printf "platform :ios, '15.1'\ntarget '%s' do\n  use_expo_modules!\nend\n" "$name" >"$p/ios/Podfile"
	printf 'PODFILE CHECKSUM: 4f1c2a\nCOCOAPODS: 1.16.2\n' >"$p/ios/Podfile.lock"
	printf '// !$*UTF8*$!\n{ archiveVersion = 1; }\n' >"$p/ios/$name.xcodeproj/project.pbxproj"
	mkdir -p "$p/ios/$name.xcworkspace"
	printf '<?xml version="1.0" encoding="UTF-8"?>\n<Workspace version = "1.0"/>\n' >"$p/ios/$name.xcworkspace/contents.xcworkspacedata"
	printf '#import <UIKit/UIKit.h>\n' >"$p/ios/$name/AppDelegate.h"
	printf "rootProject.name = '%s'\ninclude ':app'\n" "$name" >"$p/android/settings.gradle"
	printf 'buildscript { }\n' >"$p/android/build.gradle"
	printf '#!/bin/sh\n' >"$p/android/gradlew"
	printf 'apply plugin: "com.android.application"\n' >"$p/android/app/build.gradle"
	printf '<manifest />\n' >"$p/android/app/src/main/AndroidManifest.xml"

	# Generated / downloaded content (ignored by git).
	for spec in "${rn_modules[@]}"; do
		mods+=("${spec%%:*}:$((${spec##*:} * scale / 100))")
	done
	npm_pkgs "$p/node_modules" "${mods[@]}"
	local pod
	for pod in React-Core:2048 hermes-engine:5120 RNReanimated:1024 React-Fabric:1536 Yoga:256 SDWebImage:640 SocketRocket:128 fmt:384 glog:192 boost:1024; do
		blob "$p/ios/Pods/${pod%%:*}/lib${pod%%:*}.a" $((${pod##*:} * scale / 100))
		printf '// header\n' >"$p/ios/Pods/${pod%%:*}/${pod%%:*}.h"
	done
	mkdir -p "$p/ios/Pods/Target Support Files" "$p/ios/Pods/Headers/Public"
	printf 'PODS_ROOT = ${SRCROOT}\n' >"$p/ios/Pods/Target Support Files/Pods-$name.debug.xcconfig"
	printf 'PODFILE CHECKSUM: 4f1c2a\n' >"$p/ios/Pods/Manifest.lock"
	blob "$p/ios/build/Build/Products/Debug-iphonesimulator/$name.app/$name" $((6144 * scale / 100))
	blob "$p/ios/build/Build/Intermediates.noindex/$name.build/Objects-normal/arm64/AppDelegate.o" $((3072 * scale / 100))
	mkdir -p "$p/ios/build/Build/XCBuildData"
	printf 'build-db\n' >"$p/ios/build/Build/XCBuildData/build.db"
	blob "$p/ios/build/ModuleCache.noindex/Foundation.pcm" $((2048 * scale / 100))
	blob "$p/android/app/build/outputs/apk/debug/app-debug.apk" $((7168 * scale / 100))
	blob "$p/android/app/build/intermediates/dex/debug/classes.dex" $((4096 * scale / 100))
	blob "$p/android/app/build/generated/source/codegen/jni/libappmodules.so" $((1536 * scale / 100))
	blob "$p/android/.gradle/8.10.2/executionHistory/executionHistory.bin" $((1536 * scale / 100))
	blob "$p/android/.gradle/8.10.2/fileHashes/fileHashes.bin" $((512 * scale / 100))
	blob "$p/.expo/web/cache/production/images/splash.png" $((1280 * scale / 100))
	printf '{}\n' >"$p/.expo/devices.json"
}

# git_init <dir>: a repository with one commit on main, pushed to a local bare "origin".
export GIT_CONFIG_GLOBAL="$H/.gitconfig" GIT_CONFIG_NOSYSTEM=1
cat >"$H/.gitconfig" <<'EOF'
[user]
	name = Demo Dev
	email = dev@example.com
[init]
	defaultBranch = main
[advice]
	detachedHead = false
EOF
git_at() { # git_at <days ago> <git args...>
	local days=$1
	shift
	local d
	d="@$((now - days * DAY)) +0000"
	GIT_AUTHOR_DATE="$d" GIT_COMMITTER_DATE="$d" git "$@" >/dev/null 2>&1
}
git_init() {
	local p=$1 days=$2 bare
	bare="$root/remotes/$(basename "$p").git"
	git init -q "$p"
	git -C "$p" add -A
	git_at "$days" -C "$p" commit -q -m "Initial commit"
	git init -q --bare "$bare"
	git -C "$p" remote add origin "$bare"
	git -C "$p" push -q -u origin main >/dev/null 2>&1
}

# ------------------------------------------------------------------ projects (~/code)
code="$H/code"
mkdir -p "$code"

rn_project "$code/my-app" MyApp 100
git_init "$code/my-app" 30
rn_project "$code/shop-app" ShopApp 70
git_init "$code/shop-app" 120

# landing-page: a Next.js site nobody touched for months.
lp="$code/landing-page"
mkdir -p "$lp/app"
printf '{ "name": "landing-page", "private": true, "scripts": { "dev": "next dev", "build": "next build" } }\n' >"$lp/package.json"
printf 'module.exports = {};\n' >"$lp/next.config.js"
printf 'export default function Page() { return null; }\n' >"$lp/app/page.tsx"
printf 'node_modules/\n.next/\n' >"$lp/.gitignore"
npm_pkgs "$lp/node_modules" next:12288 react:512 react-dom:768 typescript:4096 tailwindcss:1536 eslint:1024 @vercel/analytics:128 sharp:3072
blob "$lp/.next/cache/webpack/client-production/0.pack" 6144
blob "$lp/.next/server/app/page.js" 512
git_init "$lp" 150

# ------------------------------------------------------------------ AI agent worktrees
# agent_modules <node_modules> <scale %>: what an agent installs in its worktree.
agent_modules() {
	local spec mods=()
	for spec in react:512 react-native:6144 metro:1536 typescript:3072 expo:640 @expo/cli:2048 jest:1024 react-native-reanimated:2048 @babel/core:1024; do
		mods+=("${spec%%:*}:$((${spec##*:} * $2 / 100))")
	done
	npm_pkgs "$1" "${mods[@]}"
}

sa="$code/shop-app"

# Codex: branch merged into main and pushed, nothing pending, idle for weeks -> recommended.
wt="$H/.codex/worktrees/7f3a/shop-app"
mkdir -p "$(dirname "$wt")"
git -C "$sa" worktree add -q -b codex/fix-cart-total "$wt" main >/dev/null 2>&1
printf 'export const total = (items) => items.reduce((s, i) => s + i.price * i.qty, 0);\n' >"$wt/src/cart.ts"
git -C "$wt" add -A
git_at 26 -C "$wt" commit -q -m "Fix cart total with quantities"
git -C "$wt" push -q -u origin codex/fix-cart-total >/dev/null 2>&1
git_at 25 -C "$sa" merge -q --no-ff -m "Merge branch 'codex/fix-cart-total'" codex/fix-cart-total
git -C "$sa" push -q origin main >/dev/null 2>&1
agent_modules "$wt/node_modules" 140

# Cursor: work in progress, uncommitted changes -> caution, git refuses to remove it.
wt="$H/.cursor/worktrees/shop-app/dark-mode"
mkdir -p "$(dirname "$wt")"
git -C "$sa" worktree add -q -b feat/dark-mode "$wt" main >/dev/null 2>&1
printf 'export const colors = { background: "#0d1117", text: "#e6edf3" };\n' >"$wt/src/theme.ts"
git -C "$wt" add -A
git_at 3 -C "$wt" commit -q -m "Add dark theme colors"
git -C "$wt" push -q -u origin feat/dark-mode >/dev/null 2>&1
printf 'export function Home() { /* TODO: use the dark theme */ return null; }\n' >"$wt/src/screens/Home.tsx"
printf 'export function ThemeToggle() { return null; }\n' >"$wt/src/components/ThemeToggle.tsx"
agent_modules "$wt/node_modules" 100

ma="$code/my-app"

# Claude Code (claude -w): two commits never pushed -> kept by default, the branch survives removal anyway.
wt="$ma/.claude/worktrees/onboarding"
git -C "$ma" worktree add -q -b worktree-onboarding "$wt" main >/dev/null 2>&1
printf 'export function Onboarding() { return null; }\n' >"$wt/src/screens/Onboarding.tsx"
git -C "$wt" add -A
git_at 9 -C "$wt" commit -q -m "Onboarding screen"
printf 'export const steps = ["welcome", "notifications", "done"];\n' >"$wt/src/screens/steps.ts"
git -C "$wt" add -A
git_at 8 -C "$wt" commit -q -m "Onboarding steps"
agent_modules "$wt/node_modules" 80
printf '.claude/worktrees/\n' >>"$ma/.git/info/exclude"

# Conductor: merged and idle, like the Codex one.
wt="$H/conductor/workspaces/my-app/lisbon"
mkdir -p "$(dirname "$wt")"
git -C "$ma" worktree add -q -b dev/lisbon "$wt" main >/dev/null 2>&1
printf 'export const API_URL = "https://api.example.com/v2";\n' >"$wt/src/api.ts"
git -C "$wt" add -A
git_at 21 -C "$wt" commit -q -m "Use the v2 API"
git -C "$wt" push -q -u origin dev/lisbon >/dev/null 2>&1
git_at 20 -C "$ma" merge -q --no-ff -m "Merge branch 'dev/lisbon'" dev/lisbon
git -C "$ma" push -q origin main >/dev/null 2>&1
agent_modules "$wt/node_modules" 120

# ------------------------------------------------------------------ caches
blob "$H/.npm/_cacache/content-v2/sha512/3a/9f/1c7e2b" 6144
blob "$H/.npm/_cacache/content-v2/sha512/b1/04/88aa01" 4096
blob "$H/.npm/_cacache/index-v5/4e/2d/7f10ac" 512
blob "$H/.npm/_npx/6f1e2d3c4b5a6978/node_modules/create-expo-app/dist/index.js" 3072
blob "$H/Library/Caches/Yarn/v6/npm-react-native-0.76.5-1a2b/node_modules/react-native/index.js" 5120
blob "$H/Library/Caches/Yarn/v6/npm-typescript-5.6.3-9c8d/node_modules/typescript/lib/typescript.js" 3072
for z in react-native-npm-0.76.5-4c1e2f.zip typescript-npm-5.6.3-7a9b0c.zip metro-npm-0.81.0-2d3e4f.zip @babel-core-npm-7.26.0-5f6a7b.zip; do
	blob "$H/.yarn/berry/cache/$z" 2048
done
blob "$H/Library/Caches/CocoaPods/Pods/Release/hermes-engine/0.76.5-2b1c/destroot/Library/Frameworks/hermes.xcframework/hermes" 5120
blob "$H/.gradle/caches/modules-2/files-2.1/com.facebook.react/react-android/0.76.5/react-android-0.76.5-release.aar" 6144
blob "$H/.gradle/caches/transforms-4/1c2d3e4f5a6b7c8d/transformed/jetified-kotlin-stdlib-2.0.21.jar" 3072
blob "$H/.gradle/caches/8.10.2/kotlin-dsl/accessors/9f8e7d6c/classes.jar" 1024
blob "$H/.gradle/daemon/8.10.2/daemon-12345.out.log" 1536

# Xcode DerivedData: one folder per workspace, named <Project>-<28 letters>.
dd="$H/Library/Developer/Xcode/DerivedData"
dd_item() { # dd_item <folder> <workspace> <KiB> <days since last build>
	local d="$dd/$1" when
	when=$(date -u -r $((now - $4 * DAY)) +%Y-%m-%dT%H:%M:%SZ)
	mkdir -p "$d"
	cat >"$d/info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>LastAccessedDate</key>
	<date>$when</date>
	<key>WorkspacePath</key>
	<string>$2</string>
</dict>
</plist>
EOF
	blob "$d/Build/Products/Debug-iphonesimulator/app.bin" $(($3 * 6 / 10))
	blob "$d/Index.noindex/DataStore/v5/records.bin" $(($3 * 3 / 10))
	blob "$d/Logs/Build/build.xcactivitylog" $(($3 / 10))
}
dd_item MyApp-bqxkzuyhmnvwjgdhcoqsyreafmtl "$code/my-app/ios/MyApp.xcworkspace" 14336 12
dd_item ShopApp-fgnhaqwuktcjzsbxlpvqymderoia "$code/shop-app/ios/ShopApp.xcworkspace" 10240 60
dd_item OldPrototype-akdjeuwnqpzmxbcvlrtysghfioe "$H/Desktop/old-prototype/ios/OldPrototype.xcworkspace" 8192 200
blob "$H/Library/Developer/Xcode/iOS DeviceSupport/iPhone16,2 18.1 (22B83)/Symbols/usr/lib/dyld" 6144

# ------------------------------------------------------------------ AI tools data
enc() { printf '%s' "$1" | tr '/.' '--'; } # Claude Code's project folder name for a path
for p in "$code/my-app" "$code/shop-app"; do
	d="$H/.claude/projects/$(enc "$p")"
	blob "$d/0b6f1c2e-5d1a-4c7e-9f3b-2a8d4e6c1f70.jsonl" 1536
	blob "$d/7e2a9c41-3b8d-4f60-a1d2-c5e7f9b0a3d6.jsonl" 1024
done
blob "$H/.claude/file-history/0b6f1c2e-5d1a-4c7e-9f3b-2a8d4e6c1f70/3f2a1b0c@v1" 1280
blob "$H/Library/Caches/claude-cli-nodejs/$(enc "$code/my-app")/mcp-logs-github/2026-08-01T10-00-00.txt" 3072
blob "$H/.codex/sessions/2026/06/12/rollout-2026-06-12T09-41-07-6f1e2d3c.jsonl" 2048
blob "$H/.codex/sessions/2026/07/03/rollout-2026-07-03T15-02-44-9a8b7c6d.jsonl" 1536
blob "$H/.codex/log/codex-tui.log" 1536

# ------------------------------------------------------------------ ages
# Old stuff first, recent stuff last (worktree checkouts share parents with the projects).
stamp 150 "$lp"
stamp 120 "$sa"
stamp 45 "$H/.npm" "$H/Library/Caches/Yarn" "$H/.yarn" "$H/Library/Caches/CocoaPods" "$H/.gradle"
stamp 60 "$dd" "$H/Library/Developer/Xcode/iOS DeviceSupport"
stamp 90 "$H/.claude" "$H/.codex" "$H/Library/Caches/claude-cli-nodejs"
stamp 30 "$ma"
stamp 25 "$H/.codex/worktrees"
stamp 20 "$H/conductor"
stamp 2 "$H/.cursor"
stamp 8 "$ma/.claude/worktrees"
stamp 1 "$ma/src" "$ma/package.json" "$ma/.git/index" "$ma/.git/HEAD" # my-app: in active development
stamp 12 "$dd/MyApp-bqxkzuyhmnvwjgdhcoqsyreafmtl"

# ------------------------------------------------------------------ PATH: binary, git, fakes
cp "$bin_src" "$root/bin/lu-cleaner"
# git: prefer a standalone git; /usr/bin/git is a shim into Xcode / the Command
# Line Tools (denied by the profile below), so it is resolved to the real binary.
git_bin=""
for c in /opt/homebrew/bin/git /usr/local/bin/git; do
	[[ -x "$c" ]] && { git_bin="$c"; break; }
done
if [[ -z "$git_bin" ]]; then
	git_bin="$(xcrun -f git 2>/dev/null || command -v git || true)"
fi
[[ -n "$git_bin" ]] || die "git not found"
ln -s "$git_bin" "$root/bin/git"
git_real="$(cd "$(dirname "$git_bin")" && pwd -P)/$(basename "$git_bin")"
git_real="$(readlink -f "$git_real" 2>/dev/null || echo "$git_real")"
git_usr="$(dirname "$(dirname "$git_real")")" # .../usr (bin/git, libexec/git-core, share)
for t in plutil sqlite3; do
	[[ -x "/usr/bin/$t" ]] && ln -s "/usr/bin/$t" "$root/bin/$t"
done
fakes=(xcrun xcodebuild xcode-select simctl docker colima container orb podman brew pnpm yarn npm bun watchman
	osascript tmutil mdfind defaults pgrep open pod node go adb emulator avdmanager sdkmanager java ollama)
for f in "${fakes[@]}"; do
	cat >"$root/bin/$f" <<EOF
#!/bin/sh
# lu-cleaner demo sandbox: no-op stand-in for $f (nothing real is ever run).
printf '%s\n' "$f \$*" >>"$root/fake-calls.log"
exit 1
EOF
	chmod +x "$root/bin/$f"
done

# ------------------------------------------------------------------ sandbox profile
real_tmp="$(/usr/bin/getconf DARWIN_USER_TEMP_DIR 2>/dev/null || true)"
real_tmp="${real_tmp%/}"
[[ -n "$real_tmp" && -d "$real_tmp" ]] && real_tmp="$(cd "$real_tmp/.." && pwd -P)" # /private/var/folders/xx/yyyy (T, C, 0...)
{
	echo '(version 1)'
	echo '(allow default)'
	echo ';; privacy: the demo sees the sandbox only, never the real home or system Xcode data'
	echo "(deny file-read* (subpath \"$real_home\"))"
	[[ -n "$real_tmp" ]] && echo "(deny file-read* (subpath \"$real_tmp\"))"
	for p in /Library /Applications /Volumes /System/Volumes/VM /private/var/vm /System/Library/AssetsV2 \
		/opt/homebrew/lib/node_modules /usr/local/lib/node_modules \
		/opt/homebrew/share/android-commandlinetools /usr/local/share/android-commandlinetools; do
		echo "(deny file-read* (subpath \"$p\"))"
	done
	# Homebrew kegs and casks: not listable (git and its libraries still load by path).
	for p in /opt/homebrew/Cellar /opt/homebrew/Caskroom /usr/local/Cellar /usr/local/Caskroom; do
		echo "(deny file-read-data (literal \"$p\"))"
	done
	echo '(deny sysctl-read (sysctl-name "vm.swapusage"))' # the real swap size
	# ps is setuid: a sandboxed process may not run it. It only lists processes
	# (the in-use guards: Xcode running...), so it runs outside the sandbox.
	echo '(allow process-exec (literal "/bin/ps") (with no-sandbox))'
	echo "(allow file-read* (subpath \"$git_usr\"))" # git itself, wherever it lives
	echo ';; safety: never run the real tools that change state'
	for p in /usr/bin/xcrun /usr/bin/xcodebuild /usr/bin/xcode-select /usr/bin/tmutil /usr/bin/osascript /usr/bin/open \
		/usr/bin/defaults /usr/bin/mdfind /opt/homebrew/bin/brew /usr/local/bin/brew /usr/local/bin/docker \
		/opt/homebrew/bin/docker /opt/homebrew/bin/pnpm /opt/homebrew/bin/watchman /usr/bin/hdiutil; do
		echo "(deny process-exec (literal \"$p\"))"
	done
	echo ';; safety: writes only inside the sandbox (and to terminals)'
	echo "(deny file-write* (require-not (require-any (subpath \"$root\") (subpath \"/dev\"))))"
} >"$root/sandbox.sb"

# ------------------------------------------------------------------ env.sh
cat >"$root/env.sh" <<EOF
# Generated by demo/setup-sandbox.sh — source it in the recording shell.
export LU_DEMO_ROOT="$root"
lu-cleaner() {
	# With LU_DEMO_BUSY_RE set (demo/record.sh), note in DIR/busy.log the guarded
	# processes (Xcode, xcodebuild...) seen while lu-cleaner runs: its in-use
	# guards hold items back then, and record.sh records the tape again. The
	# sampler is double-forked so that the shell prints no job messages.
	if [ -n "\${LU_DEMO_BUSY_RE:-}" ]; then
		( (while :; do /bin/ps -axo comm= | awk -F/ '{print \$NF}' | grep -xE "\$LU_DEMO_BUSY_RE" >>"$root/busy.log"; sleep 0.5; done) &
			echo \$! >"$root/busy.pid" )
	fi
	/usr/bin/env -i \\
		HOME="$H" USER=dev LOGNAME=dev SHELL=/bin/bash \\
		PATH="$root/bin" TMPDIR="$root/tmp/" \\
		XDG_CONFIG_HOME="$H/.config" XDG_STATE_HOME="$H/.local/state" \\
		XDG_CACHE_HOME="$H/.cache" XDG_DATA_HOME="$H/.local/share" \\
		GIT_CONFIG_GLOBAL="$H/.gitconfig" GIT_CONFIG_NOSYSTEM=1 \\
		TERM="\${TERM:-xterm-256color}" COLORTERM="\${COLORTERM:-truecolor}" LANG=en_US.UTF-8 \\
		LU_NO_CACHE=1 LU_ASSUME_FULL_DISK_ACCESS=1 \\
		/usr/bin/sandbox-exec -f "$root/sandbox.sb" "$root/bin/lu-cleaner" "\$@"
	local rc=\$?
	if [ -f "$root/busy.pid" ]; then
		kill "\$(cat "$root/busy.pid")" 2>/dev/null
		rm -f "$root/busy.pid"
	fi
	return \$rc
}
alias luc=lu-cleaner
cd "$H/code"
EOF

echo "$root"
