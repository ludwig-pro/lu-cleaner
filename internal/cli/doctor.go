package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/sysx"
)

// blocker is an app whose running process prevents (part of) a cleanup.
type blocker struct {
	label  string
	names  []string // sysx.Running patterns: exact basename, or substring of the path when it has a "/"
	impact string
}

var blockers = []blocker{
	{"Xcode", []string{"Xcode"}, "DerivedData, Archives and device support are in use"},
	{"Simulator", []string{"Simulator", "launchd_sim"}, "booted simulators and runtimes cannot be deleted"},
	{"Android emulator", []string{"qemu-system-aarch64", "qemu-system-x86_64", "emulator"}, "running AVDs cannot be deleted"},
	{"Android Studio", []string{"/Android Studio.app/Contents/MacOS/"}, "Gradle and SDK files are in use"},
	{"Cursor", []string{"/Cursor.app/Contents/MacOS/"}, "its caches and worktrees are in use"},
	{"VS Code", []string{"/Visual Studio Code.app/Contents/MacOS/"}, "its caches are in use"},
	{"Docker", []string{"com.docker.backend", "Docker Desktop", "Docker"}, "images and volumes live in its VM disk (prune them with docker)"},
	{"Codex", []string{"codex", "Codex"}, "an agent may be working in a worktree"},
	{"Claude", []string{"claude", "Claude"}, "Claude Code or the Claude app may be working in a worktree"},
}

type doctorTrash struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Files    int64  `json:"files"`
	Readable bool   `json:"readable"`
	Note     string `json:"note,omitempty"`
}

type doctorProc struct {
	App       string   `json:"app"`
	Processes []string `json:"processes"`
	Impact    string   `json:"impact"`
}

type doctorCategory struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Size        int64  `json:"size"`
	Recommended int64  `json:"recommended"`
	Items       int    `json:"items"`
	// Shared is the part of Size inside items of another category (counted
	// once in the total).
	Shared int64 `json:"shared,omitempty"`
}

type doctorReport struct {
	Version     string            `json:"version"`
	GeneratedAt time.Time         `json:"generated_at"`
	Disk        sysx.Disk         `json:"disk"`
	UsedPct     float64           `json:"used_pct"`
	Snapshots   []string          `json:"snapshots"`
	Trash       doctorTrash       `json:"trash"`
	Running     []doctorProc      `json:"running"`
	Scanned     bool              `json:"scanned"`
	Categories  []doctorCategory  `json:"categories"`
	Total       int64             `json:"total"`
	Recommended int64             `json:"recommended"`
	Errors      map[string]string `json:"errors,omitempty"`
	UseTrash    bool              `json:"use_trash"`
	Tips        []string          `json:"tips"`
}

func (c *cli) doctorCmd() *cobra.Command {
	var noScan bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Disk health: snapshots, Trash, blocking apps, and why space is not freed",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := c.doctor(cmd.Context(), !noScan)
			if err != nil {
				return err
			}
			if c.f.json {
				return c.writeJSON(r)
			}
			c.printDoctor(r)
			return nil
		},
	}
	cmd.Flags().BoolVar(&noScan, "no-scan", false, "skip the scan summary by category")
	return cmd
}

func (c *cli) doctor(ctx context.Context, scan bool) (*doctorReport, error) {
	s, err := c.newSetup(nil)
	if err != nil {
		return nil, err
	}
	env := s.env
	r := &doctorReport{
		Version:     c.Version,
		GeneratedAt: env.Now.UTC().Truncate(time.Second),
		Snapshots:   []string{},
		Running:     []doctorProc{},
		Categories:  []doctorCategory{},
		UseTrash:    s.cfg.UseTrash,
	}
	if d, err := c.Disk(env.Home); err == nil {
		r.Disk = d
		r.UsedPct = float64(int(d.UsedPct()*10)) / 10
	}

	sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	if snaps := c.Snapshots(sctx); snaps != nil {
		r.Snapshots = snaps
	}
	cancel()

	r.Trash.Path = filepath.Join(env.Home, ".Trash")
	st, err := fsx.Size(ctx, r.Trash.Path, nil)
	switch {
	case err != nil && fsx.Exists(r.Trash.Path):
		r.Trash.Note = "cannot read the Trash (grant Full Disk Access to your terminal)"
	case err != nil:
		r.Trash.Readable = true // no Trash at all
	case st.Errors > 0 && st.Files == 0:
		r.Trash.Note = "cannot read the Trash (grant Full Disk Access to your terminal)"
	case st.Errors > 0:
		r.Trash.Size, r.Trash.Files = st.Bytes, st.Files
		r.Trash.Note = "some entries are unreadable: grant Full Disk Access to your terminal for the full size"
	default:
		r.Trash.Size, r.Trash.Files, r.Trash.Readable = st.Bytes, st.Files, true
	}

	for _, b := range blockers {
		if hit := c.Running(b.names...); len(hit) > 0 {
			r.Running = append(r.Running, doctorProc{App: b.label, Processes: hit, Impact: b.impact})
		}
	}

	if scan {
		f, err := c.buildFilter(s, modeDisplay, nil, nil)
		if err != nil {
			return nil, err
		}
		res := c.collect(ctx, s, providersFor(c.Providers(), f.Categories))
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		r.Scanned = true
		groups := groupByCategory(displayItems(res.Items, f, env.Now), env.Now, s.staleAfter, "size")
		for _, g := range groups {
			r.Categories = append(r.Categories, doctorCategory{
				ID: string(g.info.ID), Title: g.info.Title, Size: g.total, Recommended: g.recoB, Items: len(g.items), Shared: g.shared,
			})
		}
		all, rec := cleanableAndRecommended(groups)
		r.Total, r.Recommended = core.Total(all), core.Total(rec)
		if len(res.Errors) > 0 {
			r.Errors = map[string]string{}
			for id, e := range res.Errors {
				r.Errors[id] = firstLine(e.Error())
			}
		}
	}
	r.Tips = doctorTips(r)
	return r, nil
}

// snapshotDate extracts "2026-09-27-101010" from a snapshot name.
func snapshotDate(name string) string {
	name = strings.TrimSuffix(name, ".local")
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[i+1:]
	}
	return name
}

func doctorTips(r *doctorReport) []string {
	snaps := "APFS local snapshots pin the blocks of deleted files until they expire (about 24h) or are thinned."
	if n := len(r.Snapshots); n > 0 {
		snaps = fmt.Sprintf("%d APFS local snapshot(s) exist: they pin the blocks of deleted files until they expire (about 24h) or are thinned.", n)
	}
	trash := "Files moved to the Trash (Finder, --trash, use_trash) keep using space until the Trash is emptied."
	if r.Trash.Size > 0 {
		trash = fmt.Sprintf("The Trash holds %s: files moved there keep using space until it is emptied.", fsx.Bytes(r.Trash.Size))
	}
	if r.UseTrash {
		trash += " use_trash = true in your config: lu-cleaner moves things there instead of deleting them."
	}
	return []string{
		snaps,
		trash,
		"Hardlinks and APFS clones: deleting one copy frees nothing while another link or clone exists (pnpm store, Xcode, copied simulators). lu-cleaner detects both: a size marked * frees less than shown (\"reclaim\" in --json is what is really freed).",
		"Open files: a running process (Simulator, Xcode, Gradle daemon, Docker, an agent in a worktree) keeps deleted files allocated until it exits. Quit the apps listed above, run './gradlew --stop'.",
		"Purgeable space: Finder counts purgeable data (snapshots, iCloud copies, caches) as available; lu-cleaner and df show the real free space. macOS frees purgeable space only on demand.",
		"Docker's disk image (Docker.raw) never shrinks by itself: prune with 'docker system prune' (and 'docker builder prune'), then restart Docker.",
	}
}

func (c *cli) printDoctor(r *doctorReport) {
	o := c.out
	bullet := "-"
	if o.tty {
		bullet = "•"
	}
	section := func(title string) { o.println(); o.println(o.paint(o.title, title)) }

	// Disk
	o.println(o.paint(o.title, "Disk"))
	if r.Disk.Total > 0 {
		pct := r.Disk.UsedPct()
		st := o.good
		switch {
		case pct >= 90:
			st = o.bad
		case pct >= 75:
			st = o.warn
		}
		o.printf("  %s %s used · %s free of %s\n",
			o.paint(st, bar(pct, 30)), o.paint(st.Bold(true), fmt.Sprintf("%.0f%%", pct)),
			o.paint(o.bold, fsx.Bytes(r.Disk.Free)), fsx.Bytes(r.Disk.Total))
	} else {
		o.println("  unknown")
	}

	// Snapshots
	section(fmt.Sprintf("APFS local snapshots (%d)", len(r.Snapshots)))
	if len(r.Snapshots) == 0 {
		o.println("  none " + o.paint(o.faint, "— deleted files free their space right away"))
	} else {
		for _, sn := range r.Snapshots {
			o.println("  " + o.paint(o.faint, sanitize(sn)))
		}
		o.println("  They pin the blocks of deleted files: that space comes back only when they")
		o.println("  expire (about 24h) or are thinned. To reclaim it now (lu-cleaner never runs these):")
		o.println("    " + o.paint(o.bold, "tmutil thinlocalsnapshots / 999999999999 4"))
		o.println("    " + o.paint(o.bold, "sudo tmutil deletelocalsnapshots "+sanitize(snapshotDate(r.Snapshots[0]))))
	}

	// Trash
	section("Trash")
	switch {
	case !r.Trash.Readable && r.Trash.Size == 0:
		o.println("  unknown " + o.paint(o.faint, "— "+sanitize(r.Trash.Note)))
	case r.Trash.Size == 0:
		o.println("  empty")
	default:
		o.printf("  %s in %s — empty it to free the space\n", o.sizeText(r.Trash.Size), plural(int(r.Trash.Files), "file", "files"))
		if r.Trash.Note != "" {
			o.println("  " + o.paint(o.faint, sanitize(r.Trash.Note)))
		}
	}

	// Blocking apps
	section("Running apps that block cleaning")
	if len(r.Running) == 0 {
		o.println("  none")
	} else {
		t := newTable("APP", "IMPACT")
		for _, p := range r.Running {
			t.add(func(col int, v string) string {
				if col == 0 {
					return o.paint(o.warn, v)
				}
				return o.paint(o.faint, v)
			}, p.App, p.Impact)
		}
		t.render(o, "  ", false)
	}

	// Scan
	if r.Scanned {
		section("Space by category")
		if len(r.Categories) == 0 {
			o.println("  nothing found")
		} else {
			t := newTable("CATEGORY", "SIZE", "ITEMS", "RECOMMENDED")
			t.right[1], t.right[2], t.right[3] = true, true, true
			for _, cat := range r.Categories {
				info := core.LookupCategory(core.Category(cat.ID))
				reco := ""
				if cat.Recommended > 0 {
					reco = fsx.Bytes(cat.Recommended)
				}
				size := cat.Size
				t.add(func(col int, v string) string {
					switch col {
					case 1:
						return o.sizeText(size)
					case 3:
						return o.paint(o.accent, v)
					}
					return v
				}, o.catTitle(info), fsx.Bytes(cat.Size), formatCount(cat.Items), reco)
			}
			t.render(o, "  ", true)
			o.printf("  %s %s · %s recommended\n", o.paint(o.bold, "Total"), o.sizeText(r.Total), o.paint(o.accent, fsx.Bytes(r.Recommended)))
			for _, cat := range r.Categories {
				if cat.Shared > 0 {
					o.println(o.paint(o.faint, "  Category sizes overlap (items inside items of another category): the total counts those bytes once."))
					break
				}
			}
			if r.Recommended > 0 {
				o.println("  → run: " + o.paint(o.bold, "lu-cleaner clean --smart"))
			}
		}
		ids := make([]string, 0, len(r.Errors))
		for id := range r.Errors {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			o.println("  " + o.paint(o.warn, "warning: provider "+sanitize(id)+": "+sanitize(r.Errors[id])))
		}
	}

	section("Why isn't my space freed?")
	for _, tip := range r.Tips {
		o.println("  " + bullet + " " + tip)
	}
}
