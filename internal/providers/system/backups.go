package system

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

// deviceBackups reports iPhone / iPad backups made by Finder. They are the
// only local copy of a device: report only, managed from Finder. The folder
// is TCC-protected: without Full Disk Access its size is unknown.
func (s *scan) deviceBackups() {
	dir := s.appSupport("MobileSync/Backup")
	if _, err := os.Lstat(dir); err != nil {
		return
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		if permissionDenied(err) {
			it := s.newItem("ios-device-backups", core.CatSystem, "iPhone/iPad backups (size unknown)", core.RiskCaution)
			it.ID = itemID(it.Kind, dir)
			it.Location = dir
			it.Method = core.MethodReport
			it.Selectable = false
			it.Note = "Finder backups of your devices (Finder > device > Manage Backups)."
			it.Warn = "needs Full Disk Access to be measured — grant it to your terminal"
			s.emitNow(it)
		}
		return
	}
	for _, e := range ents {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		info := s.readPlist(filepath.Join(p, "Info.plist"))
		name := info["Device Name"]
		if name == "" {
			name = e.Name()
		}
		it := s.newItem("ios-device-backup", core.CatSystem, "Device backup · "+name, core.RiskCaution)
		it.Path = p
		it.Meta = map[string]string{}
		for _, k := range []string{"Product Type", "Product Version", "Last Backup Date"} {
			if v := info[k]; v != "" {
				it.Meta[strings.ToLower(strings.ReplaceAll(k, " ", "_"))] = v
			}
		}
		if t, err := time.Parse(time.RFC3339, info["Last Backup Date"]); err == nil {
			it.LastUsed = t
		}
		it.Note = "Only local backup of this device; delete it from Finder (device > Manage Backups) once it is backed up elsewhere."
		s.report(it, pubOpts{newest: it.LastUsed.IsZero()})
	}
}

// readPlist returns the top-level string / date / number values of a plist
// file (XML natively; binary through plutil).
func (s *scan) readPlist(path string) map[string]string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(f, 4<<20))
	f.Close()
	if err != nil {
		return nil
	}
	if bytes.HasPrefix(data, []byte("bplist")) {
		if !s.has("plutil") {
			return nil
		}
		out, err := s.run(5*time.Second, "plutil", "-convert", "json", "-o", "-", path)
		if err != nil {
			return nil
		}
		var m map[string]any
		if json.Unmarshal(out, &m) != nil {
			return nil
		}
		res := map[string]string{}
		for k, v := range m {
			switch x := v.(type) {
			case string:
				res[k] = x
			case float64, bool:
				b, _ := json.Marshal(x)
				res[k] = string(b)
			}
		}
		return res
	}
	return parseXMLPlist(data)
}

// parseXMLPlist extracts the scalar values of the top-level dict of an XML
// plist. Nested dicts / arrays are skipped.
func parseXMLPlist(data []byte) map[string]string {
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = false
	res := map[string]string{}
	depth := 0 // dict depth
	key := ""
	for {
		tok, err := d.Token()
		if err != nil {
			return res
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "plist":
			case "dict":
				depth++
				if depth > 1 {
					_ = d.Skip()
					depth--
					key = ""
				}
			case "array", "data":
				_ = d.Skip()
				key = ""
			case "key":
				var v string
				if d.DecodeElement(&v, &t) == nil {
					key = v
				}
			case "true", "false":
				if depth == 1 && key != "" {
					res[key] = t.Name.Local
				}
				_ = d.Skip()
				key = ""
			default: // string, date, integer, real
				var v string
				if d.DecodeElement(&v, &t) == nil && depth == 1 && key != "" {
					res[key] = strings.TrimSpace(v)
				}
				key = ""
			}
		case xml.EndElement:
			if t.Name.Local == "dict" {
				depth--
			}
		}
	}
}
