package main

import (
	"forcefield/internal/session"
	"log"
)

func main() {
	s := session.New()

	s.AddMessage("user", "hello")
	s.AddMessage("assistant", "hi 👋")

	if err := s.Save(); err != nil {
		log.Fatal(err)
	}
}
