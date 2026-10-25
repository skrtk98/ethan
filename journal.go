package ethan

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// JournalFormat is the version of the events' JSON form, which Replay and
// Engine.Resume read back. It is raised when a change would make an
// earlier journal read differently; Replay and Resume reject journals of
// other formats rather than misread them.
const JournalFormat = 1

// ErrJournalFormat is returned by Replay and Engine.Resume for a journal
// written in a format other than JournalFormat, or with none.
var ErrJournalFormat = errors.New("ethan: unsupported journal format")

// Journal records the events of a Run as they happen, so that the Run can
// be traced and resumed after it stops (see Replay and Engine.Resume). The
// engine appends one event at a time. When Append fails the Run is
// canceled: a journal with gaps cannot be resumed from.
type Journal interface {
	Append(Event) error
}

// FileJournal is a Journal that appends events to a file as JSON Lines and
// syncs each one to disk.
type FileJournal struct {
	f *os.File
}

// NewJournal creates a journal for a new run at path, failing if the file
// already exists, so that two runs cannot share a journal.
func NewJournal(path string) (*FileJournal, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &FileJournal{f: f}, nil
}

// CreateJournal opens path for appending, creating it if needed. Appending
// to an existing journal continues it, which is what Engine.Resume expects;
// a last line cut short is removed first so that it does not run into the
// next event. Use NewJournal for a new run, and do not resume the same
// journal from two processes at once.
func CreateJournal(path string) (*FileJournal, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	if err := dropTornLine(f); err != nil {
		f.Close()
		return nil, err
	}
	return &FileJournal{f: f}, nil
}

// dropTornLine truncates f after its last newline.
func dropTornLine(f *os.File) error {
	b, err := io.ReadAll(io.NewSectionReader(f, 0, 1<<62))
	if err != nil {
		return err
	}
	if len(b) == 0 || b[len(b)-1] == '\n' {
		return nil
	}
	return f.Truncate(int64(bytes.LastIndexByte(b, '\n') + 1))
}

// Append implements Journal.
func (j *FileJournal) Append(e Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := j.f.Write(append(b, '\n')); err != nil {
		return err
	}
	return j.f.Sync()
}

// Close closes the file.
func (j *FileJournal) Close() error { return j.f.Close() }

// ReadJournal reads the events of a journal file. A last line cut short,
// as when the process died while writing it, is ignored.
func ReadJournal(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return DecodeJournal(f)
}

// DecodeJournal reads events written as JSON Lines. A last line cut short
// is ignored.
func DecodeJournal(r io.Reader) ([]Event, error) {
	var events []Event
	br := bufio.NewReader(r)
	for n := 1; ; n++ {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			var e Event
			if jerr := json.Unmarshal(line, &e); jerr != nil {
				if errors.Is(err, io.EOF) {
					return events, nil // a torn last line
				}
				return nil, fmt.Errorf("journal line %d: %w", n, jerr)
			}
			events = append(events, e)
		}
		if errors.Is(err, io.EOF) {
			return events, nil
		}
		if err != nil {
			return nil, err
		}
	}
}
