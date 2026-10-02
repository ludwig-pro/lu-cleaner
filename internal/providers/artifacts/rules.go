package artifacts

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

// rule describes one kind of project artifact.
//
// While walking, a directory entry named like one of Names is matched when
// one of Markers exists next to it (in the same parent directory). When Sub
// is set, the artifact is that child of the matched directory (".yarn" +
// "cache" → .yarn/cache) and the matched directory itself is never an
// artifact.
type rule struct {
	Kind string
	// AltKind replaces Kind outside an Android context (plain Gradle/JVM
	// projects): "android-build" → "gradle-build".
	AltKind string
	Label   string   // human description used in notes
	Names   []string // base names, exact or filepath.Match globs
	Sub     string   // artifact is Sub inside the matched directory
	// Markers are globs over the entries of the matched directory's parent;
	// at least one must exist. Empty = no marker needed.
	Markers []string
	// Content are globs over the artifact's own entries (proof that it is an
	// output: CACHEDIR.TAG, XCBuildData, lcov.info...).
	Content []string
	// NeedContent: the artifact must match Content, always.
	NeedContent bool
	// Generic names (build, dist, out, target, vendor, .yarn/*...) may be
	// source code: inside a git work tree they must be ignored by git (or,
	// with ContentBeatsIgnore, untracked with matching Content); outside git
	// they need matching Content.
	Generic bool
	// ContentBeatsIgnore: strong Content markers (Xcode build products,
	// CACHEDIR.TAG, pyvenv.cfg...) replace the "ignored by git" requirement
	// of Generic rules (Xcode build/ folders are often not ignored).
	ContentBeatsIgnore bool
	// FreeOutsideGit: outside a git work tree no Content is needed (config
	// extra_artifacts).
	FreeOutsideGit bool
	// Strong lists the Content globs that prove generator output on their own
	// (asset-manifest.json, source maps, _next...); hashed bundle names
	// (main.3f2a1b9c.js, index-B1a2C3d4.js) count too. When set, a Generic
	// match outside git whose content is only weak (*.js, index.html, assets —
	// what hand-written sources also hold) becomes a caution item that is
	// never preselected.
	Strong []string
	// NestedGitOK: the artifact legitimately holds git clones (SwiftPM
	// checkouts in DerivedData, pip editable installs, Mix/Bundler git
	// dependencies). Other Generic artifacts holding a checkout (a folder
	// with a .git entry of any type) are report-only.
	NestedGitOK bool
	// NestedGitUnder lists the first-level folders (globs) of the artifact
	// where clones are expected even without NestedGitOK: CMake FetchContent
	// (_deps/<name>-src) and ExternalProject (<name>-prefix/src/<name>).
	NestedGitUnder []string
	// Group: every match of a project forms one group item (python caches).
	Group bool
	// ExtraRoots: also applied in the extra roots (Codex chat folders,
	// Conductor archives...), which only get dependency / build rules.
	ExtraRoots bool

	Risk         core.Risk
	ProcessGuard []string
	Note         string // what it is + how it comes back (1 sentence)
}

var (
	gradleMarkers  = []string{"build.gradle", "build.gradle.kts"}
	gradleProject  = []string{"settings.gradle", "settings.gradle.kts", "gradlew", "build.gradle", "build.gradle.kts"}
	xcodeMarkers   = []string{"*.xcodeproj", "*.xcworkspace", "Podfile"}
	pyMarkers      = []string{"pyproject.toml", "requirements*.txt", "setup.py", "setup.cfg", "Pipfile", "uv.lock", "poetry.lock", "tox.ini", "noxfile.py"}
	xcodeBuildOut  = []string{"XCBuildData", "Build", "*-iphonesimulator", "*-iphoneos", "*-maccatalyst", "*-appletvsimulator", "*-xrsimulator", "Debug", "Release", "Debug-*", "Release-*", "Intermediates.noindex", "Products", "DerivedData", "*.build", "info.plist", "Logs", "ModuleCache.noindex", "SourcePackages", "Index.noindex", "generated", "EagerLinkingTBDs"}
	gradleBuildOut = []string{"intermediates", "outputs", "generated", "tmp", "kotlin", "reports", "classes", "libs", ".transforms", "snapshot", "cxx", "test-results", "resources", "kspCaches", "kotlinToolingMetadata"}
	jsBuildOut     = []string{"index.html", "*.html", "static", "*.js", "*.mjs", "*.cjs", "*.css", "*.map", "asset-manifest.json", "assets", "_expo", "bundles", "metadata.json", "*.d.ts", "aarch64-*", "armv7-*", "x86_64-*", "*.apk", "*.aab", "*.ipa", "_next", "server", "client"}
	// jsStrongOut is the part of jsBuildOut (plus a few generator-specific
	// names) that only a bundler / exporter writes. The rest (*.js, *.html,
	// static, assets, server, client...) is also what hand-written sources
	// look like: a vue-cli 2 build/ folder of webpack configs, an Electron
	// buildResources folder, a webpack starter's hand-written dist/index.html.
	jsStrongOut = []string{"asset-manifest.json", "_next", "_expo", "_astro", "_app", ".vite", "*.map", "*.LICENSE.txt", "*.apk", "*.aab", "*.ipa", "aarch64-*", "armv7-*", "x86_64-*", "builder-effective-config.yaml"}
)

// rules is the ordered rule table: for a given directory name, the first
// rule whose markers match wins (except Sub rules, which may all match).
var rules = []*rule{
	// ---------------------------------------------------------------- JS / React Native
	{
		Kind: "node_modules", Label: "node_modules", Names: []string{"node_modules"},
		Markers: []string{"package.json"}, Risk: core.RiskModerate, ExtraRoots: true,
		Note: "Installed JS dependencies",
	},
	{
		Kind: "ios-pods", Label: "CocoaPods", Names: []string{"Pods"},
		Markers: []string{"Podfile", "Podfile.lock"}, Risk: core.RiskModerate, ExtraRoots: true,
		ProcessGuard: []string{"xcodebuild"},
		Note:         "CocoaPods dependencies of the iOS app; restored by `pod install` (or `npx pod-install`, `npx expo run:ios`), 1-5 min with a warm CocoaPods cache.",
	},
	{
		Kind: "cmake-build", Label: "CMake build tree", Names: []string{"build", "build-*", "build_*", "cmake-build-*", "out"},
		Markers: []string{"CMakeLists.txt"}, Content: []string{"CMakeCache.txt"}, NeedContent: true,
		Generic: true, ContentBeatsIgnore: true, NestedGitUnder: []string{"_deps", "*-prefix"}, Risk: core.RiskModerate, ExtraRoots: true,
		Note: "CMake build tree (CMakeCache.txt); re-created by running cmake and the build again (can take minutes).",
	},
	{
		Kind: "ios-build", AltKind: "xcode-build", Label: "Xcode build folder", Names: []string{"build"},
		Markers: xcodeMarkers, Content: xcodeBuildOut, Generic: true, ContentBeatsIgnore: true, NestedGitOK: true,
		Risk: core.RiskSafe, ProcessGuard: []string{"xcodebuild"}, ExtraRoots: true,
		Note: "Xcode build products; rebuilt by the next Xcode / `expo run:ios` build.",
	},
	{
		Kind: "android-build", AltKind: "gradle-build", Label: "Gradle build folder", Names: []string{"build"},
		Markers: gradleMarkers, Content: gradleBuildOut, Generic: true, ContentBeatsIgnore: true,
		Risk: core.RiskSafe, ExtraRoots: true,
		Note: "Gradle build outputs (intermediates, APKs, codegen); rebuilt by the next Gradle / `expo run:android` build (3-10 min for a React Native app).",
	},
	{
		Kind: "flutter-build", Label: "Flutter build folder", Names: []string{"build"},
		Markers: []string{"pubspec.yaml"}, Content: []string{"app", "ios", "web", "macos", "flutter_assets", "outputs", "intermediates", "native_assets", "*.cache.dill*", "flutter_build"},
		Generic: true, Risk: core.RiskSafe, ExtraRoots: true,
		Note: "Flutter build outputs; rebuilt by `flutter build` / `flutter run`.",
	},
	{
		Kind: "js-build", Label: "JS build output", Names: []string{"build"},
		Markers: []string{"package.json"}, Content: jsBuildOut, Strong: jsStrongOut, Generic: true, Risk: core.RiskSafe, ExtraRoots: true,
		Note: "JS build output; rebuilt by the project's build script.",
	},
	{
		Kind: "android-gradle", AltKind: "gradle-cache", Label: "project Gradle state", Names: []string{".gradle"},
		Markers: gradleProject, Risk: core.RiskSafe, ExtraRoots: true,
		Note: "Project-level Gradle state (configuration cache, file hashes); recreated by the next Gradle run.",
	},
	{
		Kind: "android-kotlin", AltKind: "gradle-kotlin", Label: "project Kotlin state", Names: []string{".kotlin"},
		Markers: gradleProject, Risk: core.RiskSafe, ExtraRoots: true,
		Note: "Kotlin 2.x daemon session data and error logs; recreated by the next Gradle build.",
	},
	{
		Kind: "android-cxx", Label: "NDK/CMake intermediates", Names: []string{".cxx", ".externalNativeBuild"},
		Markers: append([]string{"CMakeLists.txt"}, gradleMarkers...), Risk: core.RiskSafe, ExtraRoots: true,
		Note: "Android NDK / CMake intermediates of native modules; rebuilt by the next native build (several minutes of C++ compilation, also fixes stale-.cxx build errors).",
	},
	{
		Kind: "expo", Label: "Expo local state", Names: []string{".expo"},
		Markers: []string{"package.json", "app.json", "app.config.*"}, Risk: core.RiskSafe, ExtraRoots: true,
		Note: "Expo local state (xcodebuild.log, devices, web cache, types); recreated by `expo start` / `expo run:*`.",
	},
	{
		Kind: "next", Label: "Next.js build cache", Names: []string{".next"},
		Markers: []string{"next.config.*", "package.json"}, Risk: core.RiskSafe, ExtraRoots: true,
		Note: "Next.js build output and cache; rebuilt by `next dev` / `next build`.",
	},
	{Kind: "open-next", Label: "OpenNext build", Names: []string{".open-next"}, Markers: []string{"package.json", "open-next.config.*"}, Risk: core.RiskSafe, Note: "OpenNext deployment bundle; rebuilt by the next OpenNext build."},
	{Kind: "nuxt", Label: "Nuxt build", Names: []string{".nuxt"}, Markers: []string{"package.json", "nuxt.config.*"}, Risk: core.RiskSafe, Note: "Nuxt dev/build output; recreated by `nuxt dev` / `nuxt build`."},
	{Kind: "nitro-output", Label: "Nitro/Nuxt output", Names: []string{".output"}, Markers: []string{"package.json"}, Generic: true, Risk: core.RiskSafe, Note: "Nitro/Nuxt production output; rebuilt by the build script."},
	{Kind: "svelte-kit", Label: "SvelteKit build", Names: []string{".svelte-kit"}, Markers: []string{"package.json", "svelte.config.*"}, Risk: core.RiskSafe, Note: "SvelteKit generated files and build output; recreated by `vite dev` / `vite build`."},
	{Kind: "astro", Label: "Astro cache", Names: []string{".astro"}, Markers: []string{"package.json", "astro.config.*"}, Risk: core.RiskSafe, Note: "Astro generated types and content cache; recreated by `astro dev` / `astro build`."},
	{Kind: "docusaurus", Label: "Docusaurus cache", Names: []string{".docusaurus"}, Markers: []string{"package.json", "docusaurus.config.*"}, Risk: core.RiskSafe, Note: "Docusaurus generated files; recreated by the next start/build."},
	{Kind: "turbo", Label: "Turborepo cache", Names: []string{".turbo"}, Markers: []string{"package.json", "turbo.json"}, Risk: core.RiskSafe, ExtraRoots: true, Note: "Turborepo local task cache; tasks just re-run uncached next time."},
	{Kind: "parcel-cache", Label: "Parcel cache", Names: []string{".parcel-cache"}, Markers: []string{"package.json"}, Risk: core.RiskSafe, Note: "Parcel build cache; recreated by the next build."},
	{Kind: "vite-cache", Label: "Vite cache", Names: []string{".vite"}, Markers: []string{"package.json", "vite.config.*"}, Risk: core.RiskSafe, Note: "Vite pre-bundled dependencies cache; recreated on the next dev start."},
	{Kind: "swc-cache", Label: "SWC cache", Names: []string{".swc"}, Markers: []string{"package.json", ".swcrc", "next.config.*"}, Risk: core.RiskSafe, Note: "SWC plugin cache; recreated automatically."},
	{Kind: "react-router", Label: "React Router generated types", Names: []string{".react-router"}, Markers: []string{"package.json", "react-router.config.*"}, Risk: core.RiskSafe, Note: "React Router generated types; recreated by the dev server / typegen."},
	{Kind: "vinxi", Label: "Vinxi build", Names: []string{".vinxi"}, Markers: []string{"package.json", "app.config.*"}, Risk: core.RiskSafe, Note: "Vinxi / TanStack Start build output; rebuilt by the next dev/build run."},
	{Kind: "tanstack", Label: "TanStack cache", Names: []string{".tanstack"}, Markers: []string{"package.json"}, Risk: core.RiskSafe, Note: "TanStack Router/Start generated files; recreated by the next dev/build run."},
	{Kind: "metro-cache", Label: "Metro cache", Names: []string{".metro-cache", ".metro"}, Markers: []string{"package.json", "metro.config.*"}, Risk: core.RiskSafe, ExtraRoots: true, Note: "Project-scoped Metro bundler cache; the first bundle after deleting it is just slower."},
	{Kind: "wireit", Label: "Wireit cache", Names: []string{".wireit"}, Markers: []string{"package.json"}, Risk: core.RiskSafe, Note: "Wireit script cache; scripts re-run uncached next time."},
	{Kind: "nyc-output", Label: "nyc coverage data", Names: []string{".nyc_output"}, Markers: []string{"package.json"}, Risk: core.RiskSafe, Note: "Raw nyc/istanbul coverage data; recreated by the next coverage run."},
	{Kind: "rollup-cache", Label: "Rollup cache", Names: []string{".rollup.cache", ".rpt2_cache"}, Markers: []string{"package.json"}, Risk: core.RiskSafe, Note: "Rollup / rollup-plugin-typescript2 cache; recreated by the next build."},
	{Kind: "storybook-static", Label: "Storybook static build", Names: []string{"storybook-static"}, Markers: []string{"package.json"}, Risk: core.RiskSafe, Note: "Static Storybook export; rebuilt by `build-storybook`."},
	{Kind: "angular-cache", Label: "Angular CLI cache", Names: []string{".angular"}, Sub: "cache", Markers: []string{"angular.json", "package.json"}, Risk: core.RiskSafe, Note: "Angular CLI build cache; recreated by the next build."},
	{Kind: "nx-cache", Label: "Nx cache", Names: []string{".nx"}, Sub: "cache", Markers: []string{"nx.json", "package.json"}, Risk: core.RiskSafe, Note: "Nx local computation cache; tasks re-run uncached next time (`nx reset` does the same)."},
	{Kind: "vercel-output", Label: "Vercel build output", Names: []string{".vercel"}, Sub: "output", Markers: []string{"package.json", "vercel.json"}, Risk: core.RiskSafe, Note: "`vercel build` output (project.json, the site link, is kept); rebuilt by `vercel build`."},
	{Kind: "netlify-cache", Label: "Netlify cache", Names: []string{".netlify"}, Sub: "cache", Markers: []string{"package.json", "netlify.toml"}, Risk: core.RiskSafe, Note: "Netlify CLI build cache (state.json, the site link, is kept); recreated by the next build."},
	{Kind: "netlify-functions-serve", Label: "Netlify functions build", Names: []string{".netlify"}, Sub: "functions-serve", Markers: []string{"package.json", "netlify.toml"}, Risk: core.RiskSafe, Note: "Netlify dev bundled functions; recreated by `netlify dev`."},
	{Kind: "wrangler-tmp", Label: "Wrangler temp build", Names: []string{".wrangler"}, Sub: "tmp", Markers: []string{"package.json", "wrangler.toml", "wrangler.json", "wrangler.jsonc"}, Risk: core.RiskSafe, Note: "Wrangler bundling scratch space (.wrangler/state, the local D1/KV data, is kept); recreated by `wrangler dev`."},
	{
		Kind: "dist", Label: "dist build output", Names: []string{"dist"},
		Markers: []string{"package.json"}, Content: jsBuildOut, Strong: jsStrongOut, Generic: true, Risk: core.RiskSafe, ExtraRoots: true,
		Note: "Build output; rebuilt by the project's build script.",
	},
	{Kind: "web-build", Label: "Expo web build", Names: []string{"web-build"}, Markers: []string{"package.json"}, Content: jsBuildOut, Strong: jsStrongOut, Generic: true, Risk: core.RiskSafe, ExtraRoots: true, Note: "Expo web export; rebuilt by `expo export`."},
	{Kind: "out", Label: "static export", Names: []string{"out"}, Markers: []string{"package.json", "next.config.*", "electron.vite.config.*"}, Content: append([]string{"main", "renderer", "preload"}, jsBuildOut...), Strong: jsStrongOut, Generic: true, Risk: core.RiskSafe, Note: "Static export / Electron build output; rebuilt by the build script."},
	{
		Kind: "coverage", Label: "coverage report", Names: []string{"coverage"},
		Markers: append([]string{"package.json"}, pyMarkers...), Content: []string{"lcov.info", "lcov-report", "coverage-final.json", "clover.xml", "coverage-summary.json", "cobertura-coverage.xml", "coverage.xml"},
		NeedContent: true, Generic: true, Risk: core.RiskSafe,
		Note: "Test coverage report; recreated by `jest --coverage` / `vitest --coverage`.",
	},
	{Kind: "htmlcov", Label: "coverage.py HTML report", Names: []string{"htmlcov"}, Markers: pyMarkers, Content: []string{"index.html", "status.json"}, NeedContent: true, Generic: true, Risk: core.RiskSafe, Note: "coverage.py HTML report; recreated by `coverage html`."},
	{
		Kind: "playwright-report", Label: "Playwright results", Names: []string{"playwright-report", "test-results", "blob-report"},
		Markers: []string{"playwright.config.*", "package.json"}, Generic: true, Risk: core.RiskModerate,
		Note: "Playwright / E2E test results (traces, videos, screenshots of the last runs); recreated by the next test run.",
	},
	{Kind: "js-cache", Label: "project .cache", Names: []string{".cache"}, Markers: []string{"package.json"}, Generic: true, Risk: core.RiskSafe, Note: "Project-level tool cache (Gatsby, babel-loader, eslint...); recreated by the next build."},
	{
		Kind: "pnpm-store", Label: "project pnpm store", Names: []string{".pnpm-store"},
		Markers: []string{"package.json", "pnpm-workspace.yaml", "pnpm-lock.yaml", ".npmrc"}, Content: []string{"v3", "v10", "v*"}, NeedContent: true,
		Risk: core.RiskModerate, Note: "pnpm content store kept inside the repo (store-dir); pnpm refetches packages on the next install, existing node_modules keep working.",
	},
	{
		Kind: "yarn-cache", Label: "Yarn Berry project cache", Names: []string{".yarn"}, Sub: "cache",
		Markers: []string{".yarnrc.yml"}, Content: []string{"*.zip"}, Generic: true, Risk: core.RiskModerate, ExtraRoots: true,
		Note: "Yarn Berry package archives; refetched by `yarn install`.",
	},
	{
		Kind: "yarn-unplugged", Label: "Yarn Berry unplugged packages", Names: []string{".yarn"}, Sub: "unplugged",
		Markers: []string{".yarnrc.yml"}, Generic: true, Risk: core.RiskModerate, ExtraRoots: true,
		Note: "Yarn Berry unplugged packages (native builds); recreated by `yarn install`.",
	},

	// ---------------------------------------------------------------- Apple
	{
		Kind: "project-derived-data", Label: "project DerivedData", Names: []string{"DerivedData", "DerivedData-*", ".derived-data*", "derived-data", "derivedData"},
		Markers: []string{"*.xcodeproj", "*.xcworkspace", "Podfile", "Package.swift"}, Content: []string{"info.plist", "Build", "ModuleCache.noindex", "Logs", "SourcePackages", "Index.noindex", "CompilationCache.noindex"},
		NeedContent: true, Generic: true, ContentBeatsIgnore: true, NestedGitOK: true, Risk: core.RiskSafe, ProcessGuard: []string{"xcodebuild"}, ExtraRoots: true,
		Note: "Project-local DerivedData (xcodebuild -derivedDataPath); rebuilt by the next build.",
	},
	{Kind: "swiftpm-build", Label: "SwiftPM .build", Names: []string{".build"}, Markers: []string{"Package.swift"}, Risk: core.RiskSafe, ExtraRoots: true, Note: "Swift Package Manager build folder and checkouts; recreated by `swift build`."},
	{Kind: "carthage-build", Label: "Carthage build", Names: []string{"Carthage"}, Sub: "Build", Markers: []string{"Cartfile", "Cartfile.resolved"}, Risk: core.RiskSafe, Note: "Carthage built frameworks; rebuilt by `carthage bootstrap --use-xcframeworks`."},

	// ---------------------------------------------------------------- other languages
	{
		Kind: "rust-target", Label: "Cargo target", Names: []string{"target"},
		Markers: []string{"Cargo.toml"}, Content: []string{"CACHEDIR.TAG", "debug", "release", ".rustc_info.json"},
		Generic: true, ContentBeatsIgnore: true, Risk: core.RiskSafe, ExtraRoots: true,
		Note: "Cargo build output; rebuilt by `cargo build` (a full recompile can be long).",
	},
	{
		Kind: "maven-target", Label: "Maven/sbt target", Names: []string{"target"},
		Markers: []string{"pom.xml", "build.sbt"}, Content: []string{"classes", "*.jar", "maven-status", "test-classes", "scala-*", "streams"},
		Generic: true, Risk: core.RiskSafe, ExtraRoots: true,
		Note: "Maven/sbt build output; rebuilt by `mvn package` / `sbt compile`.",
	},
	{Kind: "dart-tool", Label: "Dart tool state", Names: []string{".dart_tool"}, Markers: []string{"pubspec.yaml"}, Risk: core.RiskSafe, ExtraRoots: true, Note: "Dart/Flutter package config and build cache; recreated by `flutter pub get`."},
	{
		Kind: "python-venv", Label: "Python virtualenv", Names: []string{".venv", "venv"},
		Markers: pyMarkers, Content: []string{"pyvenv.cfg"}, NeedContent: true, Generic: true, ContentBeatsIgnore: true, NestedGitOK: true,
		Risk: core.RiskModerate, ExtraRoots: true,
		Note: "Python virtualenv; recreated by `uv sync` or `python -m venv .venv && pip install -r requirements.txt` (packages installed by hand are lost).",
	},
	{
		// A virtualenv without any requirements file next to it: its packages
		// were installed by hand and cannot be restored automatically.
		Kind: "python-venv", Label: "Python virtualenv", Names: []string{".venv", "venv", "env", ".env"},
		Content: []string{"pyvenv.cfg"}, NeedContent: true, Risk: core.RiskCaution, ExtraRoots: true,
		Note: "Python virtualenv without a requirements file next to it: its packages were installed by hand and must be reinstalled the same way.",
	},
	{Kind: "python-tox", Label: "tox/nox environments", Names: []string{".tox", ".nox"}, Markers: pyMarkers, Risk: core.RiskSafe, Note: "tox/nox test environments; recreated by the next tox/nox run."},
	{
		Kind: "python-cache", Label: "Python caches", Names: []string{"__pycache__", ".pytest_cache", ".mypy_cache", ".ruff_cache", ".pytype", ".pyre"},
		Group: true, Risk: core.RiskSafe, ExtraRoots: true,
		Note: "Python bytecode and tool caches (__pycache__, pytest, mypy, ruff); recreated automatically on the next run.",
	},
	{
		Kind: "ruby-vendor-bundle", Label: "Bundler vendored gems", Names: []string{"vendor"}, Sub: "bundle",
		Markers: []string{"Gemfile"}, Content: []string{"ruby", "jruby"}, Generic: true, ContentBeatsIgnore: true, NestedGitOK: true, Risk: core.RiskModerate,
		Note: "Gems installed by Bundler into vendor/bundle (CocoaPods, fastlane); reinstalled by `bundle install`.",
	},
	{Kind: "elixir-build", Label: "Mix _build", Names: []string{"_build"}, Markers: []string{"mix.exs"}, Generic: true, Risk: core.RiskSafe, Note: "Elixir compiled output; rebuilt by `mix compile`."},
	{Kind: "elixir-deps", Label: "Mix deps", Names: []string{"deps"}, Markers: []string{"mix.exs"}, Generic: true, NestedGitOK: true, Risk: core.RiskModerate, Note: "Elixir dependencies; refetched by `mix deps.get`."},
	{Kind: "zig-cache", Label: "Zig cache/output", Names: []string{"zig-cache", ".zig-cache", "zig-out"}, Markers: []string{"build.zig"}, Generic: true, Risk: core.RiskSafe, Note: "Zig build cache and output; rebuilt by `zig build`."},
	{Kind: "haskell-stack-work", Label: "Stack work dir", Names: []string{".stack-work"}, Markers: []string{"stack.yaml"}, Risk: core.RiskSafe, Note: "Haskell Stack build output; rebuilt by `stack build`."},
	{Kind: "cabal-dist", Label: "Cabal dist-newstyle", Names: []string{"dist-newstyle"}, Markers: []string{"cabal.project", "*.cabal"}, Risk: core.RiskSafe, Note: "Cabal build output; rebuilt by `cabal build`."},
	{
		Kind: "dotnet-build", Label: ".NET bin/obj", Names: []string{"bin", "obj"},
		Markers: []string{"*.csproj", "*.fsproj", "*.vbproj"}, Content: []string{"Debug", "Release", "project.assets.json"}, NeedContent: true, Generic: true,
		Risk: core.RiskSafe, Note: ".NET build output; rebuilt by `dotnet build`.",
	},
}

// looseVenv matches a directory of any name holding pyvenv.cfg (found while
// walking it), cacheDirTag one holding a valid CACHEDIR.TAG.
var (
	looseVenv = &rule{
		Kind: "python-venv", Label: "Python virtualenv", Content: []string{"pyvenv.cfg"}, NeedContent: true,
		Risk: core.RiskCaution, ExtraRoots: true,
		Note: "Python virtualenv (pyvenv.cfg) outside the usual .venv name: recreate it with `python -m venv` and reinstall its packages.",
	}
	cacheDirTag = &rule{
		Kind: "cachedir-tag", Label: "tagged cache", Content: []string{"CACHEDIR.TAG"}, NeedContent: true,
		Risk: core.RiskSafe, ExtraRoots: true,
		Note: "Folder tagged as a cache (CACHEDIR.TAG, written by cargo, uv, ccache...): recreated by the tool that owns it.",
	}
)

// cacheDirSignature starts every valid CACHEDIR.TAG (bford.info/cachedir).
const cacheDirSignature = "Signature: 8a477f597d28d172789f06886806bc55"

// validCacheDirTag checks the CACHEDIR.TAG signature of dir.
func validCacheDirTag(dir string) bool {
	fi, err := os.Lstat(filepath.Join(dir, "CACHEDIR.TAG"))
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	b, err := readSmall(filepath.Join(dir, "CACHEDIR.TAG"), int64(len(cacheDirSignature)))
	return err == nil && string(b) == cacheDirSignature
}

// Item kinds that no rule of the table carries.
const (
	kindExtra      = "extra-artifact" // config extra_artifacts
	kindIgnoredDir = "ignored-dir"    // git-ignored folder that is no known artifact
)

// Kinds returns every item kind the artifacts provider can emit, sorted and
// without duplicates: the kinds of the rule table, the names they take
// outside an Android / iOS folder (gradle-build, xcode-build...), loose
// virtualenvs, CACHEDIR.TAG folders, config extra_artifacts and git-ignored
// folders. The CLI validates `-t` with it.
func Kinds() []string {
	seen := map[string]bool{kindIgnoredDir: true}
	all := append(append([]*rule(nil), rules...), looseVenv, cacheDirTag, extraRule("x"))
	for _, r := range all {
		seen[r.Kind] = true
		if r.AltKind != "" {
			seen[r.AltKind] = true
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// extraRule builds the rule of a config extra_artifacts name.
func extraRule(name string) *rule {
	return &rule{
		Kind: kindExtra, Label: name, Names: []string{name},
		Generic: true, FreeOutsideGit: true, Risk: core.RiskModerate,
		Note: "Listed in extra_artifacts (config).",
	}
}

// validExtraName filters config extra_artifacts entries.
func validExtraName(n string) bool {
	n = strings.TrimSpace(n)
	switch n {
	case "", ".", "..", ".git", "src", "source", "app", "apps", "lib", "packages", "ios", "android":
		return false
	}
	return !strings.ContainsAny(n, "/\x00")
}

// ruleSet indexes rules by directory name.
type ruleSet struct {
	exact map[string][]*rule
	globs []*rule // rules having at least one glob name
	all   []*rule
	names map[string]bool // every exact artifact name (activity ignores them)
}

func newRuleSet(extra []string) *ruleSet {
	rs := &ruleSet{exact: map[string][]*rule{}, names: map[string]bool{}}
	add := func(r *rule) {
		rs.all = append(rs.all, r)
		glob := false
		for _, n := range r.Names {
			if hasMeta(n) {
				glob = true
				continue
			}
			rs.exact[n] = append(rs.exact[n], r)
			rs.names[n] = true
		}
		if glob {
			rs.globs = append(rs.globs, r)
		}
	}
	for _, r := range rules {
		add(r)
	}
	seen := map[string]bool{}
	for _, n := range extra {
		n = strings.TrimSpace(n)
		if !validExtraName(n) || seen[n] {
			continue
		}
		seen[n] = true
		add(extraRule(n))
	}
	return rs
}

// candidates returns the rules that may match a directory named name, in
// table order.
func (rs *ruleSet) candidates(name string) []*rule {
	ex := rs.exact[name]
	var gl []*rule
	for _, r := range rs.globs {
		for _, n := range r.Names {
			if hasMeta(n) {
				if ok, _ := filepath.Match(n, name); ok {
					gl = append(gl, r)
					break
				}
			}
		}
	}
	if len(gl) == 0 {
		return ex
	}
	if len(ex) == 0 {
		return gl
	}
	// Merge back into table order.
	pos := map[*rule]int{}
	for i, r := range rs.all {
		pos[r] = i
	}
	seen := map[*rule]bool{}
	var out []*rule
	for _, r := range append(append([]*rule{}, ex...), gl...) {
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	sortByPos(out, pos)
	return out
}

func sortByPos(rs []*rule, pos map[*rule]int) {
	for i := 1; i < len(rs); i++ {
		for j := i; j > 0 && pos[rs[j]] < pos[rs[j-1]]; j-- {
			rs[j], rs[j-1] = rs[j-1], rs[j]
		}
	}
}

func hasMeta(s string) bool { return strings.ContainsAny(s, "*?[") }

// matchAny reports whether one of the globs matches one of names.
func matchAny(globs []string, names map[string]bool) (string, bool) {
	for _, g := range globs {
		if !hasMeta(g) {
			if names[g] {
				return g, true
			}
			continue
		}
		for n := range names {
			if ok, _ := filepath.Match(g, n); ok {
				return n, true
			}
		}
	}
	return "", false
}

// markersOK checks the rule markers against the entries of the parent dir.
func (r *rule) markersOK(parentNames map[string]bool) bool {
	if len(r.Markers) == 0 {
		return true
	}
	_, ok := matchAny(r.Markers, parentNames)
	return ok
}

// requireSibling returns the RequireSibling globs of an artifact produced by
// r: markers are relative to the artifact's parent, so Sub rules need "../".
func (r *rule) requireSibling() []string {
	if len(r.Markers) == 0 {
		return nil
	}
	if r.Sub == "" {
		return append([]string(nil), r.Markers...)
	}
	prefix := strings.Repeat("../", strings.Count(r.Sub, "/")+1)
	out := make([]string, len(r.Markers))
	for i, m := range r.Markers {
		out[i] = prefix + m
	}
	return out
}

// kindFor returns the item kind for an artifact at path (AltKind outside an
// Android folder).
func (r *rule) kindFor(path string) string {
	if r.AltKind == "" {
		return r.Kind
	}
	switch r.Kind {
	case "ios-build":
		if b := filepath.Base(filepath.Dir(path)); b == "ios" || b == "macos" {
			return r.Kind
		}
		return r.AltKind
	default: // Gradle rules
		if strings.Contains(path, "/android/") {
			return r.Kind
		}
		return r.AltKind
	}
}

// strongOutput reports whether the entries of an artifact (dir) prove that a
// generator wrote it: one of r.Strong, or bundles with content-hashed names
// at the top or in the usual asset folders.
func (r *rule) strongOutput(ctx context.Context, dir string, content map[string]bool) bool {
	if _, ok := matchAny(r.Strong, content); ok {
		return true
	}
	if hashedAssetIn(content) {
		return true
	}
	for _, sub := range []string{"assets", "static/js", "static/css", "js", "css"} {
		if first, _, _ := strings.Cut(sub, "/"); !content[first] {
			continue
		}
		if hashedAssetIn(dirNames(ctx, filepath.Join(dir, sub))) {
			return true
		}
	}
	return false
}

func hashedAssetIn(names map[string]bool) bool {
	for n := range names {
		if hashedAsset(n) {
			return true
		}
	}
	return false
}

// hashedAsset reports whether name looks like a bundle with a content hash:
// webpack / CRA ("main.3f2a1b9c.js", "787.a1b2c3d4.chunk.js": hex after a
// dot) or Rollup / Vite ("index-B1a2C3d4.js": 8 base64url characters after a
// dash). Both hashes must mix digits and letters, which keeps hand-written
// names such as "app-settings.js" or "jquery.min.js" out, and date or
// timestamp stamped ones too ("release-20240101.js", "notes-24-01-01.js",
// "app.1700000000.js"). A real hash has no letter only by chance: such a
// bundle alone then leaves the folder "weak" (caution), the safe side.
func hashedAsset(name string) bool {
	ext := filepath.Ext(name)
	switch ext {
	case ".js", ".mjs", ".cjs", ".css":
	default:
		return false
	}
	stem := strings.TrimSuffix(strings.TrimSuffix(name, ext), ".chunk")
	mixed := func(h string) bool {
		return strings.ContainsAny(h, "0123456789") &&
			strings.ContainsFunc(h, func(c rune) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' })
	}
	// webpack: <name>.<hex>
	if i := strings.LastIndexByte(stem, '.'); i > 0 {
		h := stem[i+1:]
		if len(h) >= 8 && len(h) <= 32 && mixed(h) && strings.Trim(h, "0123456789abcdef") == "" {
			return true
		}
	}
	// Rollup: <name>-<8 base64url chars> (the hash itself may hold '-' or '_').
	if len(stem) > 9 && stem[len(stem)-9] == '-' {
		h := stem[len(stem)-8:]
		ok := mixed(h)
		for _, c := range h {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				ok = false
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// hasGitEntry reports whether dir holds a .git entry of any type: a git
// repository, a linked worktree or a submodule (a .git file), or a symlink;
// or whether dir is itself a bare repository (what safety.Guard refuses
// too). Such a directory is a checkout, never an artifact. An unreadable dir
// counts as one (nothing proves it is not).
func hasGitEntry(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil || !errors.Is(err, fs.ErrNotExist) || isBareRepo(dir)
}

// isBareRepo reports a bare repository layout: HEAD file, objects/ and refs/.
func isBareRepo(dir string) bool {
	h, err := os.Lstat(filepath.Join(dir, "HEAD"))
	if err != nil || !h.Mode().IsRegular() {
		return false
	}
	for _, sub := range []string{"objects", "refs"} {
		if fi, err := os.Lstat(filepath.Join(dir, sub)); err != nil || !fi.IsDir() {
			return false
		}
	}
	return true
}

// dirNames lists the entries of dir as a set (nil on error).
func dirNames(ctx context.Context, dir string) map[string]bool {
	names, err := fsx.ReadNames(ctx, dir)
	if err != nil {
		return nil
	}
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return set
}
