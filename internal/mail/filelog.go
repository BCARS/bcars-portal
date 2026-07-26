package mail

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

// FilelogSender writes each message as a JSON file in Dir. Used for local
// development and tests — no external SMTP dependency required.
type FilelogSender struct {
	Dir string
	seq atomic.Int64
}

// NewFilelogSender creates a sender that writes to dir. The directory must
// exist or Send will return an error.
func NewFilelogSender(dir string) *FilelogSender {
	return &FilelogSender{Dir: dir}
}

type filelogEntry struct {
	SentAt  string  `json:"sent_at"`
	Message Message `json:"message"`
}

// Send writes the message to a timestamped JSON file in Dir.
func (f *FilelogSender) Send(_ context.Context, msg Message) error {
	entry := filelogEntry{
		SentAt:  time.Now().UTC().Format(time.RFC3339Nano),
		Message: msg,
	}

	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return fmt.Errorf("mail/filelog: marshal: %w", err)
	}

	n := f.seq.Add(1)
	name := fmt.Sprintf("%s_%04d_%s.json",
		time.Now().UTC().Format("20060102T150405"),
		n,
		msg.TemplateID,
	)
	path := filepath.Join(f.Dir, name)

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("mail/filelog: write %s: %w", path, err)
	}
	return nil
}

// ReadAll reads all filelog JSON entries from Dir, most recent last.
// Useful in tests to inspect sent messages.
func (f *FilelogSender) ReadAll() ([]filelogEntry, error) {
	entries, err := os.ReadDir(f.Dir)
	if err != nil {
		return nil, err
	}

	var result []filelogEntry
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(f.Dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var entry filelogEntry
		if err := json.Unmarshal(data, &entry); err != nil {
			return nil, err
		}
		result = append(result, entry)
	}
	return result, nil
}
