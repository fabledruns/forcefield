package main

import (
	"context"
	"fmt"
	"log"

	"forcefield/internal/runtime"
)

func main() {
	rt, err := runtime.New()
	if err != nil {
		log.Fatal(err)
	}

	events, err := rt.Stream(context.Background(), "Say hello in five words.")
	if err != nil {
		log.Fatal(err)
	}

	for event := range events {
		if event.Err != nil {
			log.Fatal(event.Err)
		}

		fmt.Print(event.Text)
	}
}