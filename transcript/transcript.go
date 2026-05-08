package transcript

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/sleepysoong/kkode/llm"
)

const MaxFileBytes = 8 << 20

type Transcript struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Turns     []Turn    `json:"turns"`
}

type Turn struct {
	At       time.Time     `json:"at"`
	Request  llm.Request   `json:"request"`
	Response *llm.Response `json:"response,omitempty"`
	Error    string        `json:"error,omitempty"`
}

func New(id string) *Transcript {
	now := time.Now().UTC()
	return &Transcript{ID: id, CreatedAt: now, UpdatedAt: now}
}

func (t *Transcript) Add(req llm.Request, resp *llm.Response, err error) {
	turn := Turn{At: time.Now().UTC(), Request: req, Response: resp}
	if err != nil {
		turn.Error = err.Error()
	}
	t.Turns = append(t.Turns, turn)
	t.UpdatedAt = turn.At
}

func Load(path string) (*Transcript, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxFileBytes {
		return nil, fmt.Errorf("transcript file must be <= %d bytes", MaxFileBytes)
	}
	var t Transcript
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

func (t *Transcript) Save(path string) error {
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	if len(b) > MaxFileBytes {
		return fmt.Errorf("transcript file must be <= %d bytes", MaxFileBytes)
	}
	return os.WriteFile(path, b, 0o644)
}

func (t *Transcript) SaveRedacted(path string) error {
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	b = []byte(llm.RedactSecrets(string(b)))
	if len(b) > MaxFileBytes {
		return fmt.Errorf("transcript file must be <= %d bytes", MaxFileBytes)
	}
	return os.WriteFile(path, b, 0o644)
}
