package main

import (
	"fmt"
	"log"

	"forcefield/internal/runtime"
)

func main() {
	fmt.Println("1. Creating runtime")

	rt, err := runtime.New()
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("2. Running prompt")

	resp, err := rt.Run("What files are in the current directory and what is my current path?")
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("3. Got response")

	fmt.Printf("%+v\n", resp)
}