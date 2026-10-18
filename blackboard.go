package ethan

import (
	"sync"
	"time"
)

// Note is an entry on the blackboard.
type Note struct {
	// Author is the ID of the task whose agent wrote the note.
	Author string    `json:"author"`
	Text   string    `json:"text"`
	Time   time.Time `json:"time"`
}

// Blackboard is the append-only memory shared by all agents of a Run.
// It is safe for concurrent use.
type Blackboard struct {
	mu    sync.Mutex
	notes []Note
}

// Post appends a note and returns it.
func (b *Blackboard) Post(author, text string) Note {
	n := Note{Author: author, Text: text, Time: time.Now()}
	b.mu.Lock()
	b.notes = append(b.notes, n)
	b.mu.Unlock()
	return n
}

// Recent returns up to n of the latest notes, oldest first.
func (b *Blackboard) Recent(n int) []Note {
	b.mu.Lock()
	defer b.mu.Unlock()
	start := max(len(b.notes)-n, 0)
	return append([]Note(nil), b.notes[start:]...)
}

// Notes returns all notes, oldest first.
func (b *Blackboard) Notes() []Note {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]Note{}, b.notes...)
}
