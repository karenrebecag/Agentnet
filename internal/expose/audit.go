package expose

import (
	"encoding/json"
	"io"
	"os"
	"time"

	"github.com/karenrebecag/agentnet/internal/filelock"
)

// Event is metadata only. Message text and filesystem paths do not belong here.
type Event struct {
	At     time.Time `json:"at"`
	Guest  string    `json:"guest"`
	Action string    `json:"action"`
	Result string    `json:"result"`
	Bytes  int       `json:"bytes"`
}

// Append writes one JSON line under the file lock.
func Append(path string, e Event) error {
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	return filelock.With(path, func(f *os.File) error {
		if _, err := f.Seek(0, io.SeekEnd); err != nil {
			return err
		}
		_, err := f.Write(line)
		return err
	})
}
