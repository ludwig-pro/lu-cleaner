package clean

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/config"
	"github.com/ludwig-pro/lu-cleaner/internal/statefile"
	"golang.org/x/sys/unix"
)

const maxHistoryBytes = 5 << 20
const maxHistoryLine = 64 << 10

func appendHistory(s *Summary) (err error) {
	d, err := statefile.OpenDir(config.StateDir())
	if err != nil {
		return err
	}
	defer d.Close()
	unlock, err := d.Lock("history.lock")
	if err != nil {
		return err
	}
	defer unlock()
	f, err := d.Open("history.jsonl", unix.O_CREAT|unix.O_APPEND|unix.O_WRONLY)
	if err != nil {
		return err
	}
	defer func() {
		if f == nil {
			return
		}
		if e := f.Close(); err == nil {
			err = e
		}
	}()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	used := st.Size()
	if used > maxHistoryBytes {
		read, e := d.Open("history.jsonl", unix.O_RDONLY)
		if e != nil {
			return e
		}
		_, e = read.Seek(used-maxHistoryBytes, io.SeekStart)
		var tail []byte
		if e == nil {
			tail, e = io.ReadAll(io.LimitReader(read, maxHistoryBytes))
		}
		read.Close()
		if e != nil {
			return e
		}
		if i := bytes.IndexByte(tail, '\n'); i >= 0 {
			tail = tail[i+1:]
		} else {
			tail = nil
		}
		if e = f.Close(); e != nil {
			return e
		}
		if e = d.Put("history.jsonl", tail); e != nil {
			return e
		}
		f, e = d.Open("history.jsonl", unix.O_APPEND|unix.O_WRONLY)
		if e != nil {
			return e
		}
		used = int64(len(tail))
	}
	now := time.Now()
	for _, r := range s.Results {
		if r.Item == nil || (r.Status == StatusSkipped && r.Message == msgGone) {
			continue
		}
		line, e := json.Marshal(historyEntry(now, r))
		if e != nil {
			return e
		}
		if len(line)+1 > maxHistoryLine {
			return fmt.Errorf("history entry exceeds size limit")
		}
		line = append(line, '\n')
		if used+int64(len(line)) > maxHistoryBytes {
			// Validate an existing archive before replacing it (never follow it).
			old, e := d.Open("history.previous.jsonl", unix.O_RDONLY)
			if e == nil {
				old.Close()
			} else if !errors.Is(e, os.ErrNotExist) {
				return e
			}
			if e = f.Close(); e != nil {
				return e
			}
			if e = d.Rename("history.jsonl", "history.previous.jsonl"); e != nil {
				return e
			}
			f, e = d.Open("history.jsonl", unix.O_CREAT|unix.O_EXCL|unix.O_APPEND|unix.O_WRONLY)
			if e != nil {
				return e
			}
			used = 0
		}
		if _, e = f.Write(line); e != nil {
			return e
		}
		used += int64(len(line))
	}
	return nil
}

// ReadHistory returns the retained history, oldest first. Both rotation files
// and legacy oversized files are read with bounded memory and input sizes.
func ReadHistory() ([]HistoryEntry, error) {
	if _, err := os.Lstat(config.StateDir()); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	d, err := statefile.OpenDir(config.StateDir())
	if err != nil {
		return nil, err
	}
	defer d.Close()
	unlock, err := d.Lock("history.lock")
	if err != nil {
		return nil, err
	}
	defer unlock()
	var out []HistoryEntry
	for _, name := range []string{"history.previous.jsonl", "history.jsonl"} {
		f, err := d.Open(name, unix.O_RDONLY)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		entries, err := readHistory(f)
		f.Close()
		if err != nil {
			return nil, err
		}
		out = append(out, entries...)
	}
	return out, nil
}

func readHistory(f *os.File) ([]HistoryEntry, error) {
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	partial := st.Size() > maxHistoryBytes
	if partial {
		if _, err := f.Seek(st.Size()-maxHistoryBytes, io.SeekStart); err != nil {
			return nil, err
		}
	}
	scan := bufio.NewScanner(io.LimitReader(f, maxHistoryBytes))
	scan.Buffer(make([]byte, 4096), maxHistoryLine)
	if partial {
		scan.Scan()
	} // omit the first partial JSONL record
	var out []HistoryEntry
	for scan.Scan() {
		var e HistoryEntry
		if json.Unmarshal(scan.Bytes(), &e) != nil {
			continue
		}
		var has struct {
			Freed *int64 `json:"freed"`
		}
		if json.Unmarshal(scan.Bytes(), &has) == nil && has.Freed == nil {
			e.Freed = legacyFreed(e)
		}
		out = append(out, e)
	}
	return out, scan.Err()
}
