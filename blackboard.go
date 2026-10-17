package ethan

import "time"

// Note is an entry on the blackboard.
type Note struct {
	// Author is the ID of the task whose agent wrote the note.
	Author string    `json:"author"`
	Text   string    `json:"text"`
	Time   time.Time `json:"time"`
}

const noteToolName = "note"
