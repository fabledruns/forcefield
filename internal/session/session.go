// Package session manages local conversation history.
package session

import (
	"forcefield/internal/providers"
	"time"
)

// Message is one session message.
type Message struct {
	Role    string    `json:"role"`
	Content string    `json:"content"`
	Time    time.Time `json:"time"`
}

// Session is a persisted conversation.
type Session struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	Messages []Message `json:"messages"`
}

// ProviderMessages converts session history to provider messages.
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
