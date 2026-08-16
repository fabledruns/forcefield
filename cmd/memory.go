package cmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"forcefield/internal/config"
	"forcefield/internal/memory"
)

// memoryCmd is the "ff memory" parent command. It has no action of its
// own; each piece of functionality lives in a subcommand below.
var memoryCmd = &cobra.Command{
	Use:   "memory",
	Short: "Manage persistent project memory",
	Long: `Memory holds durable, project-specific facts that Forcefield remembers
across sessions instead of starting from zero every time. The current
project is identified by its Git repository root, falling back to the
current directory outside a repo.`,
}

var memoryListCmd = &cobra.Command{
	Use:   "list",
	Short: "List remembered facts for the current project",
	Args:  cobra.NoArgs,

	RunE: func(cmd *cobra.Command, args []string) error {
		store, err := currentProjectMemoryStore()
		if err != nil {
			return err
		}

		entries, err := store.Load()
		if err != nil {
			return err
		}

		if len(entries) == 0 {
			fmt.Println("No memory entries for this project yet.")
			return nil
		}

		for _, e := range entries {
			fmt.Printf("%s  %s  %s\n", e.ID, e.CreatedAt.Format("2006-01-02 15:04"), e.Text)
		}
		return nil
	},
}

var memoryAddCmd = &cobra.Command{
	Use:   "add [text]",
	Short: "Remember a new fact about the current project",
	Args:  cobra.MinimumNArgs(1),

	RunE: func(cmd *cobra.Command, args []string) error {
		text := strings.TrimSpace(strings.Join(args, " "))

		store, err := currentProjectMemoryStore()
		if err != nil {
			return err
		}

		entry, added, err := store.Add(text)
		if err != nil {
			return err
		}

		if !added {
			fmt.Printf("Already remembered (id: %s): %s\n", entry.ID, entry.Text)
			return nil
		}

		fmt.Printf("Remembered (id: %s): %s\n", entry.ID, entry.Text)
		return nil
	},
}

var memoryRemoveCmd = &cobra.Command{
	Use:   "remove [id]",
	Short: "Remove a remembered fact by its id",
	Args:  cobra.ExactArgs(1),

	RunE: func(cmd *cobra.Command, args []string) error {
		store, err := currentProjectMemoryStore()
		if err != nil {
			return err
		}

		id := args[0]
		if err := store.Remove(id); err != nil {
			if errors.Is(err, memory.ErrNotFound) {
				return fmt.Errorf("no memory entry with id %q", id)
			}
			return err
		}

		fmt.Printf("Removed memory entry %s\n", id)
		return nil
	},
}

var memoryClearCmd = &cobra.Command{
	Use:   "clear",
	Short: "Remove all remembered facts for the current project",
	Args:  cobra.NoArgs,

	RunE: func(cmd *cobra.Command, args []string) error {
		store, err := currentProjectMemoryStore()
		if err != nil {
			return err
		}

		if err := store.Clear(); err != nil {
			return err
		}

		fmt.Println("Cleared project memory.")
		return nil
	},
}

// currentProjectMemoryStore resolves the Forcefield home directory and
// returns the memory store for the project rooted at the current
// working directory (its Git root, or the directory itself outside a
// repo).
func currentProjectMemoryStore() (*memory.Store, error) {
	home, err := config.Dir()
	if err != nil {
		return nil, err
	}
	return memory.CurrentProjectStore(home)
}

func init() {
	memoryCmd.AddCommand(memoryListCmd)
	memoryCmd.AddCommand(memoryAddCmd)
	memoryCmd.AddCommand(memoryRemoveCmd)
	memoryCmd.AddCommand(memoryClearCmd)
	rootCmd.AddCommand(memoryCmd)
}
