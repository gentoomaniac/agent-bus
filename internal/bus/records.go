package bus

import "time"

// TopicRecord is the value of a record in `agents.topics`, keyed by topic name.
type TopicRecord struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	UpdatedAt   time.Time `json:"updated_at"`
}
