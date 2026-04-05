package model

import "time"

// LabelVocabulary represents an allowed label key:value pair in the controlled vocabulary.
type LabelVocabulary struct {
	ID          string    `json:"id"`
	Key         string    `json:"key"`
	Value       string    `json:"value"`
	Description string    `json:"description,omitempty"`
	Color       string    `json:"color,omitempty"` // Tailwind color name: blue, green, purple, etc.
	CreatedAt   time.Time `json:"created_at"`
}
