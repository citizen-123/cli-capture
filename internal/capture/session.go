package capture

import (
	"encoding/json"
	"io"
	"os"

	"github.com/citizen-123/cli-capture/internal/ownerfile"
)

// Save writes flows to w as indented JSON — a capture session that can be
// reloaded later with Load.
func Save(w io.Writer, flows []*Flow) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(flows)
}

// SaveFile writes a capture session to path. The session holds request and
// response bodies verbatim, credentials included, so on POSIX it is
// owner-only (0600) and atomically replaces any previous session.
func SaveFile(path string, flows []*Flow) error {
	return ownerfile.WriteFunc(path, func(w io.Writer) error {
		return Save(w, flows)
	})
}

// Load reads a capture session previously written by Save.
func Load(r io.Reader) ([]*Flow, error) {
	var flows []*Flow
	if err := json.NewDecoder(r).Decode(&flows); err != nil {
		return nil, err
	}
	return flows, nil
}

// LoadFile reads a capture session from path.
func LoadFile(path string) ([]*Flow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Load(f)
}
