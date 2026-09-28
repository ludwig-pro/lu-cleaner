package catalog

import (
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

// System domain: static, well-known locations (app logs, browser and
// Electron app caches, editor caches, language toolchain caches, container
// tool caches). Everything that needs logic (Trash, Docker / colima / Lima
// VMs, Homebrew, Go, rustup, rbenv, VS Code, Zed, JetBrains, Downloads, app
// updater leftovers, device backups, Time Machine snapshots) lives in
// internal/providers/system.
//
// Owned elsewhere (never listed here): ~/Library/Caches/{Yarn,ms-playwright,
// ReactNative,dotslash,node-gyp,bun,pnpm,deno,mise,Cypress,...} (js), Codex /
// Claude / Cursor / ChatGPT / Windsurf / Raycast data (ai), CocoaPods,
// SwiftPM, com.apple.dt.* (apple), Android Studio / Gradle / ~/.m2 (android).

// chromiumBrowser describes a Chromium-based browser's cache locations.
type chromiumBrowser struct {
	id, name string
	caches   string // ~/Library/Caches/... (holds <profile>/Cache and <profile>/Code Cache)
	data     string // ~/Library/Application Support/... (User Data dir)
	proc     []string
}

// browserEntries returns the cache (safe), service-worker cache (moderate)
// and on-device model (moderate) entries of a Chromium browser. Profiles
// (cookies, logins, history, extensions) are never matched: only named cache
// folders are.
func browserEntries(b chromiumBrowser) []Entry {
	caches := []string{
		b.caches + "/*/Cache",
		b.caches + "/*/Code Cache",
		b.data + "/*/GPUCache",
		b.data + "/*/DawnCache",
		b.data + "/*/DawnGraphiteCache",
		b.data + "/*/DawnWebGPUCache",
		b.data + "/GrShaderCache",
		b.data + "/ShaderCache",
		b.data + "/GraphiteDawnCache",
		b.data + "/GPUPersistentCache",
		b.data + "/component_crx_cache",
		b.data + "/extensions_crx_cache",
	}
	return []Entry{
		{
			ID: "browser-" + b.id + "-cache", Category: core.CatSystem,
			Name:         b.name + " caches",
			Paths:        caches,
			Risk:         core.RiskSafe,
			ProcessGuard: b.proc,
			Note:         b.name + " HTTP, JavaScript code, GPU / shader and component download caches (never the profile: logins, history and extensions are kept); rebuilt while browsing.",
		},
		{
			ID: "browser-" + b.id + "-service-worker-cache", Category: core.CatSystem,
			Name:         b.name + " site offline caches (Service Worker)",
			Paths:        []string{b.data + "/*/Service Worker/CacheStorage"},
			Risk:         core.RiskModerate,
			ProcessGuard: b.proc,
			Note:         "Offline copies of web apps kept by their service workers (" + b.name + "); sites download them again, installed web apps lose offline data.",
		},
		{
			ID: "browser-" + b.id + "-models", Category: core.CatSystem,
			Name: b.name + " on-device AI models",
			Paths: []string{
				b.data + "/OptGuideOnDeviceModel",
				b.data + "/OptGuideOnDeviceClassifierModel",
				b.data + "/optimization_guide_model_store",
				b.data + "/screen_ai",
			},
			Risk:         core.RiskModerate,
			ProcessGuard: b.proc,
			Note:         "Machine-learning models " + b.name + " downloads in the background (on-device AI, screen reader OCR, page classification); downloaded again when a feature needs them.",
		},
	}
}

// electronApp describes a desktop app built on Electron / WebView2.
type electronApp struct {
	id, name string
	dir      string // app data dir (glob allowed)
	proc     []string
}

// electronEntries returns the Chromium cache entry (safe) and the service
// worker cache entry (moderate) of an Electron app.
func electronEntries(a electronApp) []Entry {
	var caches []string
	for _, sub := range []string{"Cache", "Code Cache", "GPUCache", "DawnCache", "DawnGraphiteCache", "DawnWebGPUCache", "GrShaderCache", "ShaderCache", "GraphiteDawnCache", "Crashpad/completed"} {
		caches = append(caches, a.dir+"/"+sub)
	}
	return []Entry{
		{
			ID: "app-" + a.id + "-cache", Category: core.CatSystem,
			Name:         a.name + " caches",
			Paths:        caches,
			Risk:         core.RiskSafe,
			ProcessGuard: a.proc,
			Note:         a.name + " web caches (HTTP, JavaScript code, GPU) and sent crash reports; rebuilt by the app, your data and login are kept.",
		},
		{
			ID: "app-" + a.id + "-service-worker-cache", Category: core.CatSystem,
			Name:         a.name + " offline web cache (Service Worker)",
			Paths:        []string{a.dir + "/Service Worker/CacheStorage"},
			Risk:         core.RiskModerate,
			ProcessGuard: a.proc,
			Note:         a.name + " offline copy of its web client; downloaded again at the next launch (slower first start).",
		},
	}
}

func init() {
	const as = "~/Library/Application Support"
	for _, b := range []chromiumBrowser{
		{"chrome", "Google Chrome", "~/Library/Caches/Google/Chrome", as + "/Google/Chrome", []string{"Google Chrome"}},
		{"dia", "Dia", "~/Library/Caches/Dia/User Data", as + "/Dia/User Data", []string{"Dia"}},
		{"arc", "Arc", "~/Library/Caches/Arc/User Data", as + "/Arc/User Data", []string{"Arc"}},
		{"brave", "Brave", "~/Library/Caches/BraveSoftware/Brave-Browser", as + "/BraveSoftware/Brave-Browser", []string{"Brave Browser"}},
		{"edge", "Microsoft Edge", "~/Library/Caches/Microsoft Edge", as + "/Microsoft Edge", []string{"Microsoft Edge"}},
		{"vivaldi", "Vivaldi", "~/Library/Caches/Vivaldi", as + "/Vivaldi", []string{"Vivaldi"}},
		{"comet", "Comet", "~/Library/Caches/Comet", as + "/Comet", []string{"Comet"}},
		{"chromium", "Chromium", "~/Library/Caches/Chromium", as + "/Chromium", []string{"Chromium"}},
	} {
		add(browserEntries(b)...)
	}
	for _, a := range []electronApp{
		{"slack", "Slack", as + "/Slack", []string{"Slack"}},
		{"discord", "Discord", as + "/discord", []string{"Discord"}},
		{"figma", "Figma", as + "/Figma/DesktopProfile/*", []string{"Figma"}},
		{"notion", "Notion", as + "/Notion", []string{"Notion"}},
		{"notion-calendar", "Notion Calendar", as + "/Notion Calendar", []string{"Notion Calendar"}},
		{"obsidian", "Obsidian", as + "/obsidian", []string{"Obsidian"}},
		{"linear", "Linear", as + "/Linear", []string{"Linear"}},
		{"postman", "Postman", as + "/Postman", []string{"Postman"}},
		{"github-desktop", "GitHub Desktop", as + "/GitHub Desktop", []string{"GitHub Desktop"}},
		{"teams", "Microsoft Teams", "~/Library/Containers/com.microsoft.teams2/Data/Library/Application Support/Microsoft/MSTeams/EBWebView/*", []string{"MSTeams", "Microsoft Teams"}},
	} {
		add(electronEntries(a)...)
	}

	add(
		// ---------------------------------------------------------- browsers (non Chromium)
		Entry{
			ID: "browser-firefox-cache", Category: core.CatSystem,
			Name: "Firefox caches",
			Paths: []string{
				"~/Library/Caches/Firefox/Profiles/*/cache2",
				"~/Library/Caches/Firefox/Profiles/*/startupCache",
				"~/Library/Caches/Firefox/Profiles/*/thumbnails",
			},
			Risk:         core.RiskSafe,
			ProcessGuard: []string{"firefox"},
			Note:         "Firefox HTTP cache, startup cache and thumbnails (the profile in Application Support is never touched); rebuilt while browsing.",
		},
		Entry{
			ID: "browser-zen-cache", Category: core.CatSystem,
			Name: "Zen browser caches",
			Paths: []string{
				"~/Library/Caches/zen/Profiles/*/cache2",
				"~/Library/Caches/zen/Profiles/*/startupCache",
			},
			Risk:         core.RiskSafe,
			ProcessGuard: []string{"zen"},
			Note:         "Zen browser HTTP and startup caches (profiles are kept); rebuilt while browsing.",
		},
		Entry{
			ID: "browser-dia-model-cache", Category: core.CatSystem,
			Name:         "Dia on-device models",
			Paths:        []string{"~/Library/Caches/company.thebrowser.dia/ModelFileCache"},
			Risk:         core.RiskModerate,
			ProcessGuard: []string{"Dia"},
			Note:         "Classification models Dia downloads for its assistant; downloaded again when needed.",
		},
		Entry{
			ID: "browser-google-updater-cache", Category: core.CatSystem,
			Name:  "Google Updater downloads",
			Paths: []string{as + "/Google/GoogleUpdater/crx_cache", as + "/Google/GoogleUpdater/*.old"},
			Risk:  core.RiskSafe,
			Note:  "Update packages and previous versions kept by Google's updater (Chrome); downloaded again when an update is pending.",
		},

		// ---------------------------------------------------------- logs
		Entry{
			ID: "system-user-logs", Category: core.CatSystem,
			Name:  "App logs & crash reports (~/Library/Logs)",
			Paths: []string{"~/Library/Logs/*"},
			// Owned by other providers or kept on purpose: CoreSimulator (apple),
			// Google/AndroidStudio (android), JetBrains + Zed (system provider),
			// Homebrew (brew cleanup), Codex (conversation indexes), mole and
			// lu-cleaner audit logs.
			Exclude:   []string{"CoreSimulator", "Google", "JetBrains", "Zed", "Homebrew", "com.openai.codex", "mole", "lu-cleaner"},
			OlderThan: 7 * 24 * time.Hour, // keep folders that just received a log or crash report
			Risk:      core.RiskSafe,
			Note:      "Log folders and crash reports untouched for a week; apps create new ones as needed (running apps keep writing to their open file).",
		},
		Entry{
			ID: "system-user-log-files", Category: core.CatSystem,
			Name:      "App log files (~/Library/Logs)",
			Paths:     []string{"~/Library/Logs/*.log", "~/Library/Logs/*.log.*"},
			Files:     true,
			OlderThan: 7 * 24 * time.Hour,
			Risk:      core.RiskSafe,
			Note:      "Loose log files in ~/Library/Logs not written for a week; recreated by the app.",
		},
		Entry{
			ID: "system-proton-bridge-logs", Category: core.CatSystem,
			Name:         "Proton Mail Bridge logs",
			Paths:        []string{as + "/protonmail/bridge-v3/logs"},
			Risk:         core.RiskSafe,
			ProcessGuard: []string{"bridge", "Proton Mail Bridge"},
			Note:         "Proton Mail Bridge log files (its local mail store and credentials are never touched); rewritten by the bridge.",
		},

		// ---------------------------------------------------------- misc app caches
		Entry{
			ID: "system-geoservices-cache", Category: core.CatSystem,
			Name:  "Apple Maps tile cache (GeoServices)",
			Paths: []string{"~/Library/Caches/GeoServices"},
			Risk:  core.RiskSafe,
			Note:  "Map tiles cached by macOS location services and Maps; downloaded again when a map is displayed.",
		},
		Entry{
			ID: "system-music-artwork-cache", Category: core.CatSystem,
			Name:         "Apple Music / TV artwork cache",
			Paths:        []string{"~/Library/Containers/com.apple.AMPArtworkAgent/Data/Documents/artwork"},
			Risk:         core.RiskModerate,
			ProcessGuard: []string{"Music", "TV"},
			Note:         "Album and show artwork cached by the Music and TV apps; downloaded again while browsing the library.",
		},
		Entry{
			ID: "system-spotify-cache", Category: core.CatSystem,
			Name:         "Spotify streaming cache",
			Paths:        []string{"~/Library/Caches/com.spotify.client"},
			Risk:         core.RiskSafe,
			ProcessGuard: []string{"Spotify"},
			Note:         "Streaming cache of the Spotify app (downloaded playlists live elsewhere and are kept); refilled while listening.",
		},
		Entry{
			ID: "system-microsoft-autoupdate-cache", Category: core.CatSystem,
			Name:  "Microsoft AutoUpdate downloads",
			Paths: []string{"~/Library/Caches/com.microsoft.autoupdate.fba", "~/Library/Caches/com.microsoft.autoupdate2"},
			Risk:  core.RiskSafe,
			Note:  "Update packages downloaded by Microsoft AutoUpdate; downloaded again when an update is pending.",
		},
		Entry{
			ID: "system-image-caches", Category: core.CatSystem,
			Name:  "Image & crash-reporter caches (Kingfisher, Sentry)",
			Paths: []string{"~/Library/Caches/com.onevcat.Kingfisher.ImageCache.*", "~/Library/Caches/io.sentry", "~/Library/Caches/SentryCrash"},
			Risk:  core.RiskSafe,
			Note:  "Image caches of apps using Kingfisher and crash reports already sent by the Sentry SDK; rebuilt by the apps.",
		},
		Entry{
			ID: "system-fontconfig-cache", Category: core.CatSystem,
			Name:  "fontconfig cache",
			Paths: []string{"~/.cache/fontconfig"},
			Risk:  core.RiskSafe,
			Note:  "Font index built by fontconfig (Homebrew tools, LibreOffice, ImageMagick...); rebuilt in seconds.",
		},
		Entry{
			ID: "system-mole-cache", Category: core.CatSystem,
			Name:  "mole analyzer cache",
			Paths: []string{"~/.cache/mole"},
			Risk:  core.RiskSafe,
			Note:  "Scan cache of the mole cleaner (its settings in ~/.config/mole are kept); rebuilt by its next analysis.",
		},
		Entry{
			ID: "system-languagetool-cache", Category: core.CatSystem,
			Name:  "LanguageTool server download (language_tool_python)",
			Paths: []string{"~/.cache/language_tool_python"},
			Risk:  core.RiskModerate,
			Note:  "LanguageTool server downloaded by the language_tool_python package; downloaded again (~400 MB) on its next use.",
		},

		// ---------------------------------------------------------- IDEs
		Entry{
			ID: "ide-sublime-text-cache", Category: core.CatIDE,
			Name: "Sublime Text caches & index",
			Paths: []string{
				"~/Library/Caches/com.sublimetext.4", "~/Library/Caches/com.sublimetext.3",
				as + "/Sublime Text/Cache", as + "/Sublime Text/Index",
				as + "/Sublime Text 3/Cache", as + "/Sublime Text 3/Index",
			},
			Risk:         core.RiskSafe,
			ProcessGuard: []string{"sublime_text"},
			Note:         "Sublime Text caches and symbol index (settings and packages are kept); rebuilt at launch.",
		},
		Entry{
			ID: "ide-sublime-merge-cache", Category: core.CatIDE,
			Name:         "Sublime Merge cache",
			Paths:        []string{"~/Library/Caches/com.sublimemerge"},
			Risk:         core.RiskSafe,
			ProcessGuard: []string{"sublime_merge"},
			Note:         "Sublime Merge cache; rebuilt at launch.",
		},
		Entry{
			ID: "ide-vscode-cpptools-cache", Category: core.CatIDE,
			Name:  "VS Code C/C++ IntelliSense cache (ipch)",
			Paths: []string{"~/Library/Caches/vscode-cpptools"},
			Risk:  core.RiskSafe,
			Note:  "Precompiled headers of the Microsoft C/C++ extension; rebuilt when a C/C++ file is opened.",
		},
		Entry{
			ID: "ide-vscode-ripgrep-cache", Category: core.CatIDE,
			Name:  "VS Code ripgrep downloads",
			Paths: []string{"~/.cache/vscode-ripgrep"},
			Risk:  core.RiskSafe,
			Note:  "ripgrep binaries downloaded by @vscode/ripgrep installs; downloaded again by the next install that needs them.",
		},

		// ---------------------------------------------------------- containers
		Entry{
			ID: "containers-vm-image-cache", Category: core.CatContainers,
			Name:  "Lima / colima downloaded VM images",
			Paths: []string{"~/Library/Caches/lima", "~/Library/Caches/colima", "~/.lima/_cache"},
			Risk:  core.RiskSafe,
			Note:  "Linux images downloaded to create Lima / colima VMs (existing VMs keep their own disks); downloaded again at the next VM creation.",
		},
		Entry{
			ID: "containers-docker-buildx-cache", Category: core.CatContainers,
			Name:  "Docker buildx local cache",
			Paths: []string{"~/.docker/buildx/cache"},
			Risk:  core.RiskSafe,
			Note:  "Local build cache exported by docker buildx (registry credentials and contexts in ~/.docker are kept); rebuilt by the next build.",
		},
		Entry{
			ID: "containers-docker-desktop-logs", Category: core.CatContainers,
			Name:         "Docker Desktop logs",
			Paths:        []string{"~/Library/Containers/com.docker.docker/Data/log"},
			Risk:         core.RiskSafe,
			ProcessGuard: []string{"Docker Desktop", "com.docker.backend"},
			Note:         "Docker Desktop VM and backend logs; rewritten while Docker Desktop runs.",
		},
		Entry{
			ID: "containers-rancher-desktop-cache", Category: core.CatContainers,
			Name:         "Rancher Desktop cache",
			Paths:        []string{"~/Library/Caches/rancher-desktop"},
			Risk:         core.RiskSafe,
			ProcessGuard: []string{"Rancher Desktop"},
			Note:         "Downloads cached by Rancher Desktop (k3s, images); downloaded again when needed.",
		},
		Entry{
			ID: "containers-tart-vagrant-cache", Category: core.CatContainers,
			Name:  "Tart / Vagrant download caches",
			Paths: []string{"~/.tart/cache", "~/.vagrant.d/tmp"},
			Risk:  core.RiskSafe,
			Note:  "Images and boxes being downloaded or cached by Tart and Vagrant; downloaded again when a VM is created.",
		},

		// ---------------------------------------------------------- Python
		Entry{
			ID: "langs-pip-cache", Category: core.CatLangs,
			Name:  "pip download cache",
			Paths: []string{"~/Library/Caches/pip", "~/.cache/pip"},
			Risk:  core.RiskSafe,
			Note:  "Wheels and HTTP responses cached by pip; downloaded again at the next install.",
		},
		Entry{
			ID: "langs-uv-cache", Category: core.CatLangs,
			Name:         "uv cache",
			Paths:        []string{"~/.cache/uv", "~/Library/Caches/uv"},
			Risk:         core.RiskSafe,
			ProcessGuard: []string{"uv"},
			Note:         "Packages and Python builds cached by uv (environments are copies, they keep working); downloaded again when needed.",
		},
		Entry{
			ID: "langs-poetry-cache", Category: core.CatLangs,
			Name:  "Poetry package cache",
			Paths: []string{"~/Library/Caches/pypoetry/cache", "~/Library/Caches/pypoetry/artifacts"},
			Risk:  core.RiskSafe,
			Note:  "Package metadata and archives cached by Poetry (its virtualenvs are never touched); downloaded again at the next install.",
		},
		Entry{
			ID: "langs-pipenv-pyenv-cache", Category: core.CatLangs,
			Name:  "pipenv / pyenv download caches",
			Paths: []string{"~/Library/Caches/pipenv", "~/.pyenv/cache"},
			Risk:  core.RiskSafe,
			Note:  "Downloads cached by pipenv and pyenv (installed Pythons are kept); downloaded again when needed.",
		},
		Entry{
			ID: "langs-conda-pkgs", Category: core.CatLangs,
			Name:     "conda package cache",
			Paths:    []string{"~/miniconda3/pkgs", "~/anaconda3/pkgs", "~/miniforge3/pkgs", "~/mambaforge/pkgs", "~/.conda/pkgs", "~/opt/miniconda3/pkgs", "~/opt/anaconda3/pkgs"},
			Risk:     core.RiskModerate,
			Method:   core.MethodCommand,
			Command:  []string{"conda", "clean", "--all", "--yes"},
			Requires: "conda",
			Note:     "Package tarballs and extracted packages cached by conda; `conda clean --all` keeps what environments need.",
		},
		Entry{
			ID: "langs-python-user-site", Category: core.CatLangs,
			Name:   "Python user packages (pip --user)",
			Paths:  []string{"~/Library/Python/*"},
			Mode:   Each,
			Risk:   core.RiskCaution,
			Method: core.MethodReport,
			Note:   "Packages installed with `pip install --user` for this Python version (often forgotten); list them with `python3 -m pip list --user` before uninstalling.",
		},

		// ---------------------------------------------------------- Rust
		Entry{
			ID: "langs-cargo-registry-cache", Category: core.CatLangs,
			Name:         "Cargo downloaded crates",
			Paths:        []string{"~/.cargo/registry/cache"},
			Risk:         core.RiskSafe,
			ProcessGuard: []string{"cargo"},
			Note:         "Compressed .crate files downloaded by cargo; downloaded again by the next build that needs them.",
		},
		Entry{
			ID: "langs-cargo-sources", Category: core.CatLangs,
			Name:         "Cargo extracted crate sources & git checkouts",
			Paths:        []string{"~/.cargo/registry/src", "~/.cargo/git/checkouts"},
			Risk:         core.RiskModerate,
			ProcessGuard: []string{"cargo", "rust-analyzer"},
			Note:         "Sources of dependencies extracted by cargo (read by rust-analyzer); re-extracted or downloaded again by the next build.",
		},
		Entry{
			ID: "langs-cargo-index", Category: core.CatLangs,
			Name:         "Cargo registry index & git databases",
			Paths:        []string{"~/.cargo/registry/index", "~/.cargo/git/db"},
			Risk:         core.RiskModerate,
			ProcessGuard: []string{"cargo"},
			Note:         "crates.io index cache and bare git repositories of git dependencies; fetched again by the next build.",
		},
		Entry{
			ID: "langs-rustup-downloads", Category: core.CatLangs,
			Name:  "rustup downloads & temp files",
			Paths: []string{"~/.rustup/downloads", "~/.rustup/tmp"},
			Risk:  core.RiskSafe,
			Note:  "Component archives downloaded by rustup and leftovers of interrupted installs (toolchains are kept).",
		},

		// ---------------------------------------------------------- Ruby
		Entry{
			ID: "langs-ruby-gem-caches", Category: core.CatLangs,
			Name: "RubyGems / Bundler download caches",
			Paths: []string{
				"~/.rbenv/versions/*/lib/ruby/gems/*/cache",
				"~/.gem/ruby/*/cache",
				"~/.gem/specs",
				"~/.bundle/cache",
				"~/.rbenv/cache",
			},
			Risk: core.RiskSafe,
			Note: "Downloaded .gem archives, gem specs and Ruby source tarballs kept after installation (installed gems keep working); downloaded again when needed.",
		},

		// ---------------------------------------------------------- .NET / JVM / others
		Entry{
			ID: "langs-nuget-http-cache", Category: core.CatLangs,
			Name:  "NuGet HTTP & plugin caches",
			Paths: []string{"~/.local/share/NuGet/http-cache", "~/.local/share/NuGet/plugins-cache", "~/.local/share/NuGet/v3-cache"},
			Risk:  core.RiskSafe,
			Note:  "HTTP responses and plugins cached by NuGet / dotnet; downloaded again at the next restore.",
		},
		Entry{
			ID: "langs-nuget-packages", Category: core.CatLangs,
			Name:  "NuGet global packages",
			Paths: []string{"~/.nuget/packages"},
			Risk:  core.RiskModerate,
			Note:  "Packages restored by dotnet / NuGet for all projects; restored again (downloaded) by the next build.",
		},
		Entry{
			ID: "langs-ivy-sbt-cache", Category: core.CatLangs,
			Name:  "Ivy / sbt caches",
			Paths: []string{"~/.ivy2/cache", "~/.sbt/boot", "~/.cache/coursier"},
			Risk:  core.RiskModerate,
			Note:  "JVM dependencies cached by Ivy, sbt and Coursier; downloaded again by the next build.",
		},
		Entry{
			ID: "langs-zig-bazel-cache", Category: core.CatLangs,
			Name:  "Zig / Bazel global caches",
			Paths: []string{"~/.cache/zig", "~/.cache/bazel", "~/Library/Caches/bazel"},
			Risk:  core.RiskModerate,
			Note:  "Global build caches of Zig and Bazel; rebuilt by the next build (can take long for big Bazel workspaces).",
		},
		Entry{
			ID: "langs-dev-cli-caches", Category: core.CatLangs,
			Name:  "Dev CLI caches (gh, pre-commit)",
			Paths: []string{"~/.cache/gh", "~/.cache/pre-commit"},
			Risk:  core.RiskModerate,
			Note:  "GitHub CLI API cache and pre-commit hook environments; recreated by the next command (pre-commit reinstalls its hooks).",
		},
	)
}
