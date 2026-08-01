// Package session provides structures and methods for managing user sessions, 
// including message handling and conversion to provider-specific formats.
// It defines the Session and Message types, along with a method to convert session messages
// into a format compatible with external providers.
package session

import (
	"forcefield/internal/providers"
	"time"
)

// Message represents a single message in a session, including its role, content, and timestamp.
type Message struct {
	Role    string    `json:"role"`
	Content string    `json:"content"`
	Time    time.Time `json:"time"`
}

// Session represents a user session, containing an ID, timestamps for 
// creation and updates, and a list of messages exchanged during the session.
type Session struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	Messages []Message `json:"messages"`
}

// ProviderMessages converts the session's messages into a slice of providers.Message,
func (s *Session) ProviderMessages() []providers.Message {
	messages := make([]providers.Message, 0, len(s.Messages))

	for _, msg := range s.Messages {
		messages = append(messages, providers.Message{
			Role:    providers.Role(msg.Role),
			Content: msg.Content,
		})
	}

	return messages
}