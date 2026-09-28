package aitools

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

// localModels proposes downloaded local models one by one (caution: they are
// deliberate multi-GB downloads). Stores symlinked to another volume become
// report-only items.
func (s *scanner) localModels() {
	s.ollamaModels()
	s.lmStudioModels()
	s.huggingFaceModels()
	s.whisperModels()
}

// ---------------------------------------------------------------- Ollama

type ollamaManifest struct {
	Config struct {
		Digest string `json:"digest"`
	} `json:"config"`
	Layers []struct {
		Digest string `json:"digest"`
	} `json:"layers"`
}

func (m *ollamaManifest) digests() []string {
	var out []string
	if m.Config.Digest != "" {
		out = append(out, m.Config.Digest)
	}
	for _, l := range m.Layers {
		if l.Digest != "" {
			out = append(out, l.Digest)
		}
	}
	return out
}

// ollamaModels emits one item per model tag: its manifest plus the blobs no
// other model references. ~/.ollama itself (it holds the ollama identity key)
// is never proposed.
func (s *scanner) ollamaModels() {
	root := s.home(".ollama/models")
	if v := os.Getenv("OLLAMA_MODELS"); filepath.IsAbs(v) {
		root = filepath.Clean(v)
	}
	if !s.usable(root) {
		return
	}
	mroot := filepath.Join(root, "manifests")
	type model struct {
		name, manifest string
		digests        []string
	}
	var models []model
	refs := map[string]int{}
	_ = filepath.WalkDir(mroot, func(p string, d fs.DirEntry, err error) error {
		if err != nil || s.ctx.Err() != nil {
			return nil
		}
		rel, _ := filepath.Rel(mroot, p)
		depth := len(strings.Split(rel, string(filepath.Separator)))
		if d.IsDir() {
			if depth > 4 && rel != "." {
				return filepath.SkipDir
			}
			return nil
		}
		if depth != 4 || strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		var m ollamaManifest
		if json.Unmarshal(data, &m) != nil || len(m.Layers) == 0 {
			return nil
		}
		parts := strings.Split(rel, string(filepath.Separator)) // registry/namespace/model/tag
		name := parts[2] + ":" + parts[3]
		switch {
		case parts[0] == "registry.ollama.ai" && parts[1] == "library":
		case parts[0] == "registry.ollama.ai":
			name = parts[1] + "/" + name
		default:
			name = parts[0] + "/" + parts[1] + "/" + name
		}
		ds := m.digests()
		for _, dg := range ds {
			refs[dg]++
		}
		models = append(models, model{name: name, manifest: p, digests: ds})
		return nil
	})
	sort.Slice(models, func(i, j int) bool { return models[i].name < models[j].name })
	for _, m := range models {
		it := s.newItem("ollama-model", core.CatAI, "Ollama model · "+m.name, core.RiskCaution)
		it.ID = itemID(it.Kind, m.manifest)
		it.Paths = []string{m.manifest}
		shared := 0
		for _, dg := range m.digests {
			if refs[dg] > 1 {
				shared++
				continue
			}
			blob := filepath.Join(root, "blobs", strings.Replace(dg, ":", "-", 1))
			if fileExists(blob) {
				it.Paths = append(it.Paths, blob)
			}
		}
		it.Location = root
		it.LastUsed = fsx.ModTime(m.manifest)
		it.ProcessGuard = procOllama
		it.Meta = map[string]string{"model": m.name}
		if shared > 0 {
			it.Meta["shared_layers_kept"] = strconv.Itoa(shared)
		}
		it.Note = "Local LLM weights pulled with `ollama pull` (re-download: several GB); layers shared with other models are kept."
		s.publish(it, pubOpts{placeholder: true})
	}
}

// ---------------------------------------------------------------- LM Studio

func (s *scanner) lmStudioModels() {
	for _, root := range []string{s.home(".lmstudio/models"), s.home(".cache/lm-studio/models")} {
		if !s.usable(root) {
			continue
		}
		for _, pub := range list(root, false) {
			if !pub.dir {
				continue
			}
			for _, m := range list(pub.path, false) {
				if !m.dir && !isDir(m.path) && !underMissingMount(m.path) { // model dirs may be symlinks to another disk
					continue
				}
				it := s.newItem("lmstudio-model", core.CatAI, "LM Studio model · "+pub.name+"/"+m.name, core.RiskCaution)
				it.ID = itemID(it.Kind, m.path)
				it.Path = m.path
				it.LastUsed = m.mtime
				it.ProcessGuard = procLMStudio
				it.Note = "Local model weights downloaded in LM Studio (re-download: several GB)."
				s.publish(it, pubOpts{placeholder: true, newest: true})
			}
		}
	}
}

// ---------------------------------------------------------------- Hugging Face

func (s *scanner) huggingFaceModels() {
	root := s.home(".cache/huggingface/hub")
	if v := os.Getenv("HF_HUB_CACHE"); filepath.IsAbs(v) {
		root = filepath.Clean(v)
	} else if v := os.Getenv("HF_HOME"); filepath.IsAbs(v) {
		root = filepath.Join(filepath.Clean(v), "hub")
	}
	if !s.usable(root) {
		return
	}
	for _, e := range list(root, false) {
		kind, rest, ok := strings.Cut(e.name, "--")
		if !e.dir || !ok || (kind != "models" && kind != "datasets" && kind != "spaces") {
			continue
		}
		repo := strings.ReplaceAll(rest, "--", "/")
		label := map[string]string{"models": "model", "datasets": "dataset", "spaces": "space"}[kind]
		it := s.newItem("huggingface-"+label, core.CatAI, "Hugging Face "+label+" · "+repo, core.RiskCaution)
		it.ID = itemID(it.Kind, e.path)
		it.Path = e.path
		it.LastUsed = e.mtime
		it.Note = "Hugging Face hub download (all revisions); re-downloaded on next use (`hf cache delete` is the interactive alternative)."
		s.publish(it, pubOpts{placeholder: true, newest: true})
	}
}

// ---------------------------------------------------------------- Whisper

func (s *scanner) whisperModels() {
	type src struct {
		kind, label, dir string
		guard            []string
	}
	for _, w := range []src{
		{"whisper-model", "Whisper model", s.home(".cache/whisper"), nil},
		{"voiceink-whisper-model", "VoiceInk Whisper model", s.appSupport("com.prakashjoshipax.VoiceInk/WhisperModels"), procVoiceInk},
	} {
		if !s.usable(w.dir) {
			continue
		}
		for _, e := range list(w.dir, false) {
			name := e.name
			if !e.dir {
				ext := filepath.Ext(name)
				if ext != ".pt" && ext != ".bin" && ext != ".gguf" && ext != ".mlmodelc" {
					continue
				}
				name = strings.TrimSuffix(name, ext)
			}
			it := s.newItem(w.kind, core.CatAI, w.label+" · "+name, core.RiskCaution)
			it.ID = itemID(it.Kind, e.path)
			it.Path = e.path
			it.LastUsed = e.mtime
			it.ProcessGuard = w.guard
			it.Note = "Speech-to-text model weights; re-downloaded on the next transcription with this model."
			s.publish(it, pubOpts{minBytes: 1 << 20})
		}
	}
}
