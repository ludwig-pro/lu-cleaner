package catalog

import (
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

// Apple domain: fixed Xcode / CoreSimulator / CocoaPods / SwiftPM / Carthage /
// fastlane locations. Everything that needs logic (DerivedData per project,
// archives, DeviceSupport, simulator devices & runtimes, device sets,
// CoreSimulator/{Caches,Temp}) lives in providers/apple.
func init() {
	add(
		// ------------------------------------------------------------ Xcode
		Entry{
			ID: "apple-xcode-module-cache", Category: core.CatXcode,
			Name:         "Xcode shared module cache",
			Paths:        []string{"~/Library/Developer/Xcode/DerivedData/*.noindex"},
			Risk:         core.RiskSafe,
			ProcessGuard: []string{"Xcode", "xcodebuild"},
			Note:         "ModuleCache.noindex / SymbolCache.noindex shared by every project: precompiled clang & Swift modules, rebuilt by the next build (slower first build).",
		},
		Entry{
			ID: "apple-xcode-caches", Category: core.CatXcode,
			Name:         "Xcode app caches",
			Paths:        []string{"~/Library/Caches/com.apple.dt.*"},
			Risk:         core.RiskSafe,
			ProcessGuard: []string{"Xcode"},
			Note:         "Caches of Xcode, xcodebuild and Instruments (~/Library/Caches/com.apple.dt.*); recreated automatically.",
		},
		Entry{
			ID: "apple-xcode-products", Category: core.CatXcode,
			Name:         "Xcode install products",
			Paths:        []string{"~/Library/Developer/Xcode/Products/*"},
			Risk:         core.RiskSafe,
			ProcessGuard: []string{"Xcode", "xcodebuild"},
			Note:         "Output of Product › Build For › Installing / xcodebuild install; recreated by the next such build.",
		},
		Entry{
			ID: "apple-xcode-docs-cache", Category: core.CatXcode,
			Name: "Xcode documentation cache",
			Paths: []string{
				"~/Library/Developer/Xcode/DocumentationCache/*",
				"~/Library/Developer/Xcode/DocumentationIndex/*",
			},
			Risk:         core.RiskSafe,
			ProcessGuard: []string{"Xcode"},
			Note:         "Downloaded developer documentation and its search index; Xcode fetches them again when the documentation window is opened.",
		},
		Entry{
			ID: "apple-dvt-downloads", Category: core.CatXcode,
			Name:         "Xcode component downloads",
			Paths:        []string{"~/Library/Developer/DVTDownloads/*"},
			Risk:         core.RiskModerate,
			ProcessGuard: []string{"Xcode", "xcodebuild"},
			Note:         "Staging area of Xcode component downloads (runtimes, Metal toolchain, personalization manifests); installed components are not affected, an interrupted download restarts.",
		},
		Entry{
			ID: "apple-device-logs", Category: core.CatXcode,
			Name: "Device crash logs imported by Xcode",
			Paths: []string{
				"~/Library/Developer/Xcode/iOS Device Logs",
				"~/Library/Developer/Xcode/watchOS Device Logs",
			},
			Risk:         core.RiskModerate,
			ProcessGuard: []string{"Xcode"},
			Note:         "Crash and device logs copied from connected devices (Devices window); Xcode imports again the logs still on the device the next time it is connected.",
		},
		Entry{
			ID: "apple-xcode-dist-staging", Category: core.CatXcode,
			Name:         "Xcode export / distribution staging",
			Paths:        []string{"$TMPDIR/XcodeDistPipeline.*", "$TMPDIR/*.xcdistributionlogs"},
			Risk:         core.RiskSafe,
			OlderThan:    24 * time.Hour,
			ProcessGuard: []string{"Xcode", "xcodebuild"},
			Note:         "Temporary copies and logs of Organizer › Distribute App / xcodebuild -exportArchive runs; recreated per export.",
		},

		// ------------------------------------------------------------ CoreSimulator
		Entry{
			ID: "apple-coresimulator-logs", Category: core.CatSimulators,
			Name:      "CoreSimulator logs",
			Paths:     []string{"~/Library/Logs/CoreSimulator/*"},
			Risk:      core.RiskSafe,
			OlderThan: 7 * 24 * time.Hour,
			Note:      "Host-side logs of CoreSimulator and of each simulator (per-UDID folders) not written for a week; recreated by simulator activity.",
		},
		Entry{
			ID: "apple-simulator-device-set-tmp", Category: core.CatSimulators,
			Name:      "Simulator device-set temp copies",
			Paths:     []string{"~/Library/Developer/CoreSimulator/Devices/device_set.plist.sb-*"},
			Files:     true,
			Risk:      core.RiskSafe,
			OlderThan: 24 * time.Hour,
			Note:      "Leftovers of interrupted atomic writes of device_set.plist (the registry itself is never touched).",
		},

		// ------------------------------------------------------------ CocoaPods
		Entry{
			ID: "apple-cocoapods-cache", Category: core.CatXcode,
			Name:  "CocoaPods download cache",
			Paths: []string{"~/Library/Caches/CocoaPods"},
			Risk:  core.RiskSafe,
			Note:  "Pod sources and podspecs downloaded by `pod install` (same as `pod cache clean --all`); the next pod install downloads what it needs again.",
		},
		Entry{
			ID: "apple-cocoapods-trunk", Category: core.CatXcode,
			Name:  "CocoaPods trunk spec index",
			Paths: []string{"~/.cocoapods/repos/trunk"},
			Risk:  core.RiskModerate,
			Note:  "CDN index of public podspecs; pod install re-fetches the specs it needs lazily (slower first install). Private spec repos are left alone.",
		},
		Entry{
			ID: "apple-cocoapods-master-repo", Category: core.CatXcode,
			Name:         "CocoaPods legacy master spec repo",
			Paths:        []string{"~/.cocoapods/repos/master", "~/.cocoapods/repos/cocoapods"},
			Mode:         Each,
			Risk:         core.RiskModerate,
			AllowGitRepo: true,
			Note:         "Git clone of the whole public Specs repo, unused since CocoaPods 1.8 switched to the CDN (`pod repo remove master`); only re-cloned if a Podfile still declares the GitHub Specs source.",
		},

		// ------------------------------------------------------------ SwiftPM / Carthage / fastlane
		Entry{
			ID: "apple-swiftpm-cache", Category: core.CatXcode,
			Name:  "Swift Package Manager cache",
			Paths: []string{"~/Library/Caches/org.swift.swiftpm"},
			Risk:  core.RiskSafe,
			Note:  "Global SwiftPM repository & artifact cache (`swift package purge-cache`); packages are cloned again on the next resolve. Mirrors and registry settings (~/Library/org.swift.swiftpm) are not touched.",
		},
		Entry{
			ID: "apple-carthage-cache", Category: core.CatXcode,
			Name:  "Carthage cache",
			Paths: []string{"~/Library/Caches/org.carthage.CarthageKit"},
			Risk:  core.RiskModerate,
			Note:  "Carthage dependency clones and downloaded binaries; `carthage bootstrap` fetches them again (slow).",
		},
		Entry{
			ID: "apple-fastlane-caches", Category: core.CatXcode,
			Name:  "fastlane caches",
			Paths: []string{"~/Library/Caches/tools.fastlane", "~/.fastlane/frameit"},
			Risk:  core.RiskModerate,
			Note:  "fastlane snapshot working files and frameit device frames; downloaded again when snapshot / frameit run. Session cookies (~/.fastlane/spaceship) are not touched.",
		},
	)
}
