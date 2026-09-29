package catalog

import (
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

// JS / React Native toolchain: package manager caches, Metro/Jest temp
// caches, Expo/EAS, browsers for testing, misc JS tool caches.
//
// Owned by the jsdev provider instead (logic needed, never listed here):
// Node versions (nvm/fnm/mise/asdf/volta), fnm_multishells, ~/.npm/_npx/*,
// pnpm stores, ~/.yarn/berry/{cache,metadata,store,index},
// ~/.expo/ios-simulator-app-cache/*, ~/Library/Caches/ms-playwright/<rev>,
// $TMPDIR React Native codegen and Vitest dirs, npm -g leftovers.
func init() {
	const day = 24 * time.Hour
	add(
		// ---------------------------------------------------------------- npm
		Entry{
			ID: "js-npm-cache", Category: core.CatJS, Name: "npm cache (_cacache)",
			Paths: []string{"~/.npm/_cacache"}, Risk: core.RiskSafe,
			Note: "npm's content cache; the next `npm install` / `npx` re-downloads what it needs. Existing node_modules are unaffected.",
		},
		Entry{
			ID: "js-npm-logs", Category: core.CatJS, Name: "npm debug logs",
			Paths: []string{"~/.npm/_logs"}, Risk: core.RiskSafe,
			Note: "Debug logs written by every npm run; npm recreates the folder.",
		},
		Entry{
			ID: "js-npm-binary-downloads", Category: core.CatJS, Name: "npm postinstall binary downloads",
			Paths: []string{"~/.npm/sentry-cli", "~/.npm/_libvips", "~/.npm/_prebuilds",
				"~/Library/Caches/sentry-cli", "~/.cache/sentry-cli"},
			Risk: core.RiskSafe,
			Note: "Binaries downloaded by postinstall scripts (@sentry/cli, sharp libvips, prebuild-install); fetched again by the next install that needs them.",
		},

		// ---------------------------------------------------------------- yarn
		Entry{
			ID: "js-yarn-classic-cache", Category: core.CatJS, Name: "Yarn classic (v1) cache",
			Paths: []string{"~/Library/Caches/Yarn/v*", "~/.cache/yarn", "~/.yarn-cache"},
			Risk:  core.RiskSafe,
			Note:  "Yarn 1 package cache; re-downloaded by the next `yarn` v1 install (node_modules are real copies and keep working). Plain `yarn cache clean` may target the Berry cache instead when corepack runs Yarn 3/4.",
		},
		Entry{
			ID: "js-yarn-tmp", Category: core.CatJS, Name: "Yarn run temp dirs",
			Paths: []string{"$TMPDIR/yarn--*"}, Risk: core.RiskSafe, OlderThan: day,
			Note: "Shim dirs created by each `yarn run` (v1) and never removed.",
		},

		// ---------------------------------------------------------------- pnpm / bun / corepack
		Entry{
			ID: "js-pnpm-metadata-cache", Category: core.CatJS, Name: "pnpm metadata cache",
			Paths: []string{"~/Library/Caches/pnpm", "~/.cache/pnpm"}, Risk: core.RiskSafe,
			Note: "Registry metadata cached by pnpm; re-fetched on the next resolution.",
		},
		Entry{
			ID: "js-bun-install-cache", Category: core.CatJS, Name: "Bun install cache",
			Paths: []string{"~/.bun/install/cache"}, Risk: core.RiskModerate,
			Note: "Packages downloaded by `bun install` (re-downloaded when needed). bun clones files into node_modules on APFS, so the real gain can be much smaller than the size shown.",
		},
		Entry{
			ID: "js-bun-transpiler-cache", Category: core.CatJS, Name: "Bun transpiler cache",
			Paths: []string{"~/Library/Caches/bun"}, Risk: core.RiskSafe,
			Note: "Runtime transpiler cache of bun; rebuilt automatically.",
		},
		Entry{
			ID: "js-bunx-tmp", Category: core.CatJS, Name: "bunx temporary installs",
			Paths: []string{"$TMPDIR/bunx-*"}, Risk: core.RiskSafe, OlderThan: day,
			Note: "Packages installed by `bunx <pkg>`; the next bunx call reinstalls them.",
		},
		Entry{
			ID: "js-corepack-cache", Category: core.CatJS, Name: "Corepack package manager downloads",
			Paths: []string{"~/.cache/node/corepack/*"}, Risk: core.RiskSafe,
			Note: "Yarn/pnpm versions downloaded by corepack (and partial corepack-* downloads); fetched again on first use. lastKnownGood.json is kept.",
		},

		// ---------------------------------------------------------------- node tooling
		Entry{
			ID: "js-node-gyp-headers", Category: core.CatJS, Name: "node-gyp Node headers",
			Paths: []string{"~/Library/Caches/node-gyp", "~/.cache/node-gyp", "~/.node-gyp", "~/.electron-gyp"},
			Risk:  core.RiskSafe,
			Note:  "Node headers downloaded to build native addons; re-downloaded (~60 MB) at the next native build.",
		},
		Entry{
			ID: "js-nvm-download-cache", Category: core.CatJS, Name: "nvm download cache",
			Paths: []string{"~/.nvm/.cache"}, Risk: core.RiskSafe,
			Note: "Node archives kept by nvm after installing; only needed to reinstall the same version offline.",
		},
		Entry{
			ID: "js-volta-inventory", Category: core.CatJS, Name: "Volta download inventory",
			Paths: []string{"~/.volta/tools/inventory"}, Risk: core.RiskSafe,
			Note: "Tarballs downloaded by Volta; installed tools keep working and Volta re-fetches on demand.",
		},
		Entry{
			ID: "js-asdf-node-downloads", Category: core.CatJS, Name: "asdf nodejs downloads",
			Paths: []string{"~/.asdf/downloads/nodejs"}, Risk: core.RiskSafe,
			Note: "Archives kept by asdf-nodejs after installing; not needed by installed versions.",
		},
		Entry{
			ID: "js-mise-cache", Category: core.CatJS, Name: "mise download cache",
			Paths: []string{"~/Library/Caches/mise", "~/.local/share/mise/downloads"}, Risk: core.RiskSafe,
			Note: "Archives and metadata cached by mise; installed tools are unaffected.",
		},
		Entry{
			ID: "js-node-compile-cache", Category: core.CatJS, Name: "Node module compile cache",
			Paths: []string{"$TMPDIR/node-compile-cache", "$TMPDIR/v8-compile-cache-*"}, Risk: core.RiskSafe,
			Note: "V8 code cache of CLIs using module.enableCompileCache (npm, AI CLIs...), one per Node version; rebuilt on the next run.",
		},
		Entry{
			ID: "js-tsx-cache", Category: core.CatJS, Name: "tsx transform cache",
			Paths: []string{"$TMPDIR/tsx-*"}, Risk: core.RiskSafe,
			Note: "Transpiled files cached by tsx; rebuilt automatically.",
		},

		// ---------------------------------------------------------------- Metro / Jest / React Native
		Entry{
			ID: "js-metro-cache", Category: core.CatJS, Name: "Metro bundler cache",
			Paths: []string{"$TMPDIR/metro-cache", "$TMPDIR/metro-bundler-cache*", "$TMPDIR/react-native-packager-cache-*"},
			Risk:  core.RiskSafe,
			Note:  "Metro transform cache shared by every RN/Expo project; rebuilt on the next bundle (first start is slower, like --reset-cache).",
		},
		Entry{
			ID: "js-metro-file-maps", Category: core.CatJS, Name: "Metro / Haste file maps",
			Paths: []string{"$TMPDIR/metro-file-map-*", "$TMPDIR/haste-map-*"}, Risk: core.RiskSafe, Files: true,
			Note: "One file map per project root (every worktree adds one); rebuilt when Metro starts.",
		},
		Entry{
			ID: "js-jest-cache", Category: core.CatJS, Name: "Jest cache",
			Paths: []string{"$TMPDIR/jest_*"}, Risk: core.RiskSafe,
			Note: "Jest transform and haste-map cache, one set per project path (every worktree adds ~100 MB); rebuilt on the next run.",
		},
		Entry{
			ID: "js-react-native-prebuilt", Category: core.CatJS, Name: "React Native prebuilt tarballs",
			Paths: []string{"~/Library/Caches/ReactNative"}, Risk: core.RiskSafe,
			Note: "React Native core/deps/Hermes prebuilt tarballs; downloaded again by the next `pod install` that needs them.",
		},
		Entry{
			ID: "js-dotslash-cache", Category: core.CatJS, Name: "DotSlash cache (React Native DevTools)",
			Paths: []string{"~/Library/Caches/dotslash"}, Risk: core.RiskSafe,
			Note: "Binaries fetched by DotSlash, e.g. React Native DevTools; re-downloaded when the debugger is opened.",
		},
		Entry{
			ID: "js-flipper-leftovers", Category: core.CatJS, Name: "Flipper leftovers",
			Paths: []string{"~/.flipper"}, Risk: core.RiskSafe, Recommended: true,
			Note: "Flipper was removed from React Native 0.74+; these files are no longer used.",
		},

		// ---------------------------------------------------------------- Expo / EAS
		Entry{
			ID: "js-expo-cli-caches", Category: core.CatJS, Name: "Expo CLI caches",
			Paths: []string{"~/.expo/native-modules-cache", "~/.expo/schema-cache", "~/.expo/versions-cache",
				"~/.expo/cache", "~/.expo/template-cache"},
			Risk: core.RiskSafe,
			Note: "Schemas, SDK versions and templates cached by expo-cli; re-fetched automatically. The Expo login (~/.expo/state.json) is never touched.",
		},
		Entry{
			ID: "js-expo-go-android", Category: core.CatJS, Name: "Expo Go Android APK cache",
			Paths: []string{"~/.expo/android-apk-cache"}, Risk: core.RiskSafe,
			Note: "Expo Go APKs downloaded by `expo start --android`; downloaded again on demand.",
		},
		Entry{
			ID: "js-eas-cli-tmp", Category: core.CatJS, Name: "EAS CLI upload archives",
			// Each upload creates its work folder right inside: a folder
			// touched within the last day may belong to an upload in progress.
			Paths: []string{"$TMPDIR/eas-cli-nodejs"}, Risk: core.RiskSafe, OlderThan: day,
			Note: "Project tarballs prepared by `eas build` / `eas update` uploads (untouched for a day); recreated on the next upload.",
		},
		Entry{
			ID: "js-eas-cli-cache", Category: core.CatJS, Name: "EAS CLI cache",
			Paths: []string{"~/Library/Caches/eas-cli"}, Risk: core.RiskSafe,
			Note: "Metadata cached by eas-cli; re-fetched automatically.",
		},
		Entry{
			ID: "js-eas-local-build", Category: core.CatJS, Name: "EAS local build workdirs",
			Paths: []string{"$TMPDIR/eas-build-local-nodejs"}, Risk: core.RiskSafe, OlderThan: 2 * time.Hour,
			Note: "Working directories of `eas build --local`; recreated per build (do not clean during a local build).",
		},

		// ---------------------------------------------------------------- test browsers
		Entry{
			ID: "js-playwright-tmp", Category: core.CatJS, Name: "Playwright temp profiles & artifacts",
			Paths: []string{"$TMPDIR/playwright-transform-cache-*", "$TMPDIR/playwright-artifacts-*",
				"$TMPDIR/playwright_chromiumdev_profile-*", "$TMPDIR/playwright_firefoxdev_profile-*",
				"$TMPDIR/playwright_webkitdev_profile-*"},
			Risk: core.RiskSafe, OlderThan: day,
			Note: "Temporary browser profiles, traces and transform cache left by Playwright runs.",
		},
		Entry{
			ID: "js-puppeteer-chrome", Category: core.CatJS, Name: "Puppeteer Chrome",
			Paths: []string{"~/.cache/puppeteer/chrome/*"}, Risk: core.RiskModerate, Mode: Each, KeepLatest: 1,
			Note: "Older Chrome builds downloaded by Puppeteer (the newest is kept); `npx puppeteer browsers install` brings one back.",
		},
		Entry{
			ID: "js-puppeteer-headless-shell", Category: core.CatJS, Name: "Puppeteer chrome-headless-shell",
			Paths: []string{"~/.cache/puppeteer/chrome-headless-shell/*"}, Risk: core.RiskModerate, Mode: Each, KeepLatest: 1,
			Note: "Older headless shells downloaded by Puppeteer (the newest is kept); re-downloaded on demand.",
		},
		Entry{
			ID: "js-puppeteer-firefox", Category: core.CatJS, Name: "Puppeteer Firefox",
			Paths: []string{"~/.cache/puppeteer/firefox/*"}, Risk: core.RiskModerate, Mode: Each, KeepLatest: 1,
			Note: "Older Firefox builds downloaded by Puppeteer (the newest is kept); re-downloaded on demand.",
		},
		Entry{
			ID: "js-cypress-binaries", Category: core.CatJS, Name: "Cypress binary",
			Paths: []string{"~/Library/Caches/Cypress/*"}, Risk: core.RiskModerate, Mode: Each, KeepLatest: 1,
			Note: "Older Cypress app versions (the newest is kept); the cypress postinstall downloads the one a project needs.",
		},
		Entry{
			ID: "js-selenium-drivers", Category: core.CatJS, Name: "Selenium Manager drivers",
			Paths: []string{"~/.cache/selenium"}, Risk: core.RiskSafe,
			Note: "Browser drivers downloaded by selenium-manager; fetched again when needed.",
		},

		// ---------------------------------------------------------------- misc JS tool caches
		Entry{
			ID: "js-electron-cache", Category: core.CatJS, Name: "Electron download cache",
			Paths: []string{"~/Library/Caches/electron", "~/Library/Caches/electron-builder", "~/.cache/electron", "~/.cache/electron-builder"},
			Risk:  core.RiskSafe,
			Note:  "Electron / electron-builder downloads; fetched again by the next install or build.",
		},
		Entry{
			ID: "js-typescript-ata", Category: core.CatJS, Name: "TypeScript type acquisition cache",
			Paths: []string{"~/Library/Caches/typescript", "~/.cache/typescript"}, Risk: core.RiskSafe,
			Note: "@types packages fetched by the editor TS server (VS Code, Cursor, Zed); re-acquired on demand.",
		},
		Entry{
			ID: "js-deno-cache", Category: core.CatJS, Name: "Deno cache",
			Paths: []string{"~/Library/Caches/deno"}, Risk: core.RiskSafe,
			Note: "Remote modules and npm packages cached by Deno (DENO_DIR); re-downloaded on the next run.",
		},
		Entry{
			ID: "js-turbo-cache", Category: core.CatJS, Name: "Turborepo global cache",
			Paths: []string{"~/Library/Caches/turbo", "~/.cache/turbo"}, Risk: core.RiskSafe,
			Note: "Global Turborepo cache (project .turbo dirs are listed with project artifacts); tasks re-run and refill it.",
		},
		Entry{
			ID: "js-esbuild-cache", Category: core.CatJS, Name: "esbuild binary cache",
			Paths: []string{"~/Library/Caches/esbuild", "~/.cache/esbuild"}, Risk: core.RiskSafe,
			Note: "esbuild binaries downloaded as install fallback; fetched again when needed.",
		},
		Entry{
			ID: "js-next-swc-cache", Category: core.CatJS, Name: "Next.js SWC binaries",
			Paths: []string{"~/Library/Caches/next-swc", "~/.cache/next-swc"}, Risk: core.RiskSafe,
			Note: "SWC binaries downloaded by Next.js as fallback; fetched again when needed.",
		},
		Entry{
			ID: "js-prisma-engines", Category: core.CatJS, Name: "Prisma engines cache",
			Paths: []string{"~/.cache/prisma"}, Risk: core.RiskSafe,
			Note: "Prisma query/schema engines; downloaded again by the next `prisma generate`.",
		},
		Entry{
			ID: "js-convex-binaries", Category: core.CatJS, Name: "Convex local backend binaries",
			Paths: []string{"~/.cache/convex/binaries", "~/.cache/convex/dashboard"}, Risk: core.RiskSafe,
			Note: "Local backend and dashboard downloaded by `npx convex dev`; fetched again for local deployments.",
		},
		Entry{
			ID: "js-giget-cache", Category: core.CatJS, Name: "giget template cache",
			Paths: []string{"~/.cache/giget"}, Risk: core.RiskSafe,
			Note: "Templates downloaded by create-* / nuxi scaffolders; fetched again when scaffolding.",
		},
	)
}
