package catalog

import "github.com/ludwig-pro/lu-cleaner/internal/core"

// Android domain: static, well-known locations. (File name: a data_android.go
// would only build for GOOS=android.) Everything that needs logic
// (SDK/AVD discovery on external volumes, AVDs, system images, NDK /
// build-tools / platforms in use, Gradle wrapper & version caches, daemon
// logs, ~/.gradle/.tmp, ~/.android caches, Android Studio caches/logs/old
// settings, JDKs) lives in internal/providers/android.

func init() {
	add(
		Entry{
			ID: "android-kotlin-daemon", Category: core.CatAndroid,
			Name:  "Kotlin daemon state (~/.kotlin)",
			Paths: []string{"~/.kotlin", "~/Library/Application Support/kotlin/daemon"},
			Risk:  core.RiskModerate,
			Note:  "Kotlin compile daemon run files, session data and logs at user level; recreated by the next Kotlin/Gradle build.",
		},
		Entry{
			ID: "android-konan-dependencies", Category: core.CatAndroid,
			Name:  "Kotlin/Native dependencies & caches (~/.konan)",
			Paths: []string{"~/.konan/dependencies", "~/.konan/cache"},
			Risk:  core.RiskModerate,
			Note:  "Kotlin Multiplatform / Kotlin/Native toolchain dependencies (LLVM, sysroots) and caches; re-downloaded by the next Kotlin/Native build (1-3 GB).",
		},
		Entry{
			ID: "android-konan-prebuilt", Category: core.CatAndroid,
			Name:       "Kotlin/Native compiler",
			Paths:      []string{"~/.konan/kotlin-native-prebuilt-*"},
			Risk:       core.RiskModerate,
			Mode:       Each,
			KeepLatest: 1,
			Note:       "Kotlin/Native compiler of one Kotlin version (the newest one is kept); re-downloaded by the next KMP build using that version.",
		},
		Entry{
			ID: "android-maven-local", Category: core.CatAndroid,
			Name:  "Maven local repository (~/.m2/repository)",
			Paths: []string{"~/.m2/repository"},
			Risk:  core.RiskCaution,
			Note:  "Maven local repository: remote artifacts are re-downloaded, but artifacts published locally (publishToMavenLocal, mvn install) cannot be; settings.xml is kept.",
		},
		Entry{
			ID: "android-skiko", Category: core.CatAndroid,
			Name:       "Skiko native libs",
			Paths:      []string{"~/.skiko/*"},
			Risk:       core.RiskSafe,
			Mode:       Each,
			KeepLatest: 1,
			Note:       "Skia native libraries extracted by Compose Desktop apps (Maestro Studio...); re-extracted on launch (the newest copy is kept).",
		},
		Entry{
			ID: "android-emulator-tmp", Category: core.CatAndroid,
			Name:         "Android emulator temp files",
			Paths:        []string{"$TMPDIR/android-*"},
			Risk:         core.RiskSafe,
			ProcessGuard: []string{"qemu-system-aarch64", "qemu-system-x86_64"},
			Note:         "Emulator temp folder (running-instance discovery files, crash reports); recreated at the next emulator launch.",
		},
		Entry{
			ID: "android-genymotion-device", Category: core.CatAndroid,
			Name:   "Genymotion virtual device",
			Paths:  []string{"~/.Genymobile/Genymotion/deployed/*"},
			Risk:   core.RiskCaution,
			Method: core.MethodReport,
			Mode:   Each,
			Note:   "Genymotion virtual device with its data; delete it from Genymotion if you no longer use it.",
		},
		Entry{
			ID: "android-maestro-apks", Category: core.CatAndroid,
			Name:   "APKs stored for Maestro",
			Paths:  []string{"~/.maestro/apps/android/*.apk"},
			Files:  true,
			Risk:   core.RiskCaution,
			Method: core.MethodReport,
			Note:   "App builds copied under ~/.maestro/apps for Maestro flows (origin unknown); rebuild or download them again before removing them by hand.",
		},
	)
}
