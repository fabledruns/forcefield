package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

// New creates a new session with a unique ID and initializes its timestamps and message list.
func New() *Session {
	now := time.Now()

	return &Session{
		ID:        uuid.NewString(),
		CreatedAt: now,
		UpdatedAt: now,
		Messages:  []Message{},
	}
}

// Save persists the session to a JSON file in the .forcefield/sessions directory, 
// updating the UpdatedAt timestamp.
func (s *Session) Save() error {
	s.UpdatedAt = time.Now()

	dir := filepath.Join(".", ".forcefield", "sessions")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	path := filepath.Join(dir, s.ID+".json")

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

// AddMessage adds a new message to the session and updates the UpdatedAt timestamp.
func (s *Session) AddMessage(role, content string) {
	s.Messages = append(s.Messages, Message{
		Role:    role,
		Content: content,
		Time:    time.Now(),
	})

	s.UpdatedAt = time.Now()
}

// Load retrieves a session from a JSON file based on its ID, 
// returning an error if the file does not exist or cannot be read.
func Load(id string) (*Session, error) {
	path := filepath.Join(".", ".forcefield", "sessions", id+".json")

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var s Session

	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}

	return &s, nil
}

// List retrieves a list of all sessions from the .forcefield/sessions directory,
// returning an error if the directory cannot be read.
func List() ([]Session, error) {
	dir := filepath.Join(".", ".forcefield", "sessions")

	files, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return []Session{}, nil
	} else if err != nil {
		return nil, err
	}

	sessions := make([]Session, 0)

	for _, file := range files {
		if filepath.Ext(file.Name()) != ".json" {
			continue
		}

		data, err := os.ReadFile(filepath.Join(dir, file.Name()))
		if err != nil {
			return nil, err
		}

		var s Session
		if err := json.Unmarshal(data, &s); err != nil {
			return nil, err
		}

		sessions = append(sessions, s)
	}

	return sessions, nil
}