package ethan

import (
	"sync"
	"time"
)

// Note is an entry on the blackboard, the append-only memory shared by all
// agents of a Run.
type Note struct {
	// Author is the ID of the task whose agent wrote the note.
	Author string    `json:"author"`
	Text   string    `json:"text"`
	Time   time.Time `json:"time"`
}

// blackboard holds the notes of a Run. It is safe for concurrent use.
type blackboard struct {
	mu    sync.Mutex
	notes []Note
}

// post appends a note and returns it.
func (b *blackboard) post(author, text string) Note {
	n := Note{Author: author, Text: text, Time: time.Now().UTC()}
	b.mu.Lock()
	b.notes = append(b.notes, n)
	b.mu.Unlock()
	return n
}

// recent returns up to n of the latest notes, oldest first.
func (b *blackboard) recent(n int) []Note {
	b.mu.Lock()
	defer b.mu.Unlock()
	start := len(b.notes) - min(max(n, 0), len(b.notes))
	return append([]Note(nil), b.notes[start:]...)
}

// all returns all notes, oldest first.
func (b *blackboard) all() []Note {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]Note{}, b.notes...)
}
