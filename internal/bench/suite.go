package bench

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed tasks/*.yaml
var embeddedTasksFS embed.FS

// Suite represents a collection of benchmark tasks.
type Suite struct {
	Tasks []*Task
}

// LoadEmbeddedTasks returns the embedded built-in benchmark tasks.
func LoadEmbeddedTasks() ([]*Task, error) {
	entries, err := fs.ReadDir(embeddedTasksFS, "tasks")
	if err != nil {
		return nil, fmt.Errorf("read embedded tasks: %w", err)
	}
	var tasks []*Task
	seenIDs := make(map[string]string)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		data, err := embeddedTasksFS.ReadFile("tasks/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("read embedded task %s: %w", e.Name(), err)
		}
		task, err := LoadTaskFromBytes(data)
		if err != nil {
			return nil, fmt.Errorf("parse embedded task %s: %w", e.Name(), err)
		}
		if existing, seen := seenIDs[task.ID]; seen {
			return nil, fmt.Errorf("duplicate task id %q in embedded %s and %s", task.ID, existing, e.Name())
		}
		seenIDs[task.ID] = e.Name()
		tasks = append(tasks, task)
	}
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].ID < tasks[j].ID
	})
	return tasks, nil
}

// DiscoverTasks scans dir recursively for task manifests (task.yaml, task.yml, or *.yaml).
// If dir does not exist or contains 0 tasks, it falls back to the embedded task suite.
func DiscoverTasks(dir string) ([]*Task, error) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		// Fall back to embedded task suite
		return LoadEmbeddedTasks()
	}

	var manifests []string
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		base := strings.ToLower(d.Name())
		if base == "task.yaml" || base == "task.yml" || base == "manifest.yaml" || base == "manifest.yml" ||
			(strings.HasSuffix(base, ".yaml") || strings.HasSuffix(base, ".yml")) {
			manifests = append(manifests, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan task directory: %w", err)
	}

	if len(manifests) == 0 {
		return LoadEmbeddedTasks()
	}

	seenIDs := make(map[string]string)
	var tasks []*Task

	for _, path := range manifests {
		task, err := LoadTask(path)
		if err != nil {
			return nil, fmt.Errorf("load task at %s: %w", path, err)
		}
		if existingPath, seen := seenIDs[task.ID]; seen {
			return nil, fmt.Errorf("duplicate task id %q found in %s and %s", task.ID, existingPath, path)
		}
		seenIDs[task.ID] = path
		tasks = append(tasks, task)
	}

	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].ID < tasks[j].ID
	})

	return tasks, nil
}

// FilterTasks selects tasks matching optional taskID, category, and difficulty criteria.
func FilterTasks(tasks []*Task, taskID, category, difficulty string) []*Task {
	taskID = strings.TrimSpace(strings.ToUpper(taskID))
	category = strings.TrimSpace(strings.ToLower(category))
	difficulty = strings.TrimSpace(strings.ToLower(difficulty))

	var filtered []*Task
	for _, t := range tasks {
		if taskID != "" && strings.ToUpper(t.ID) != taskID {
			continue
		}
		if category != "" && strings.ToLower(t.Category) != category {
			continue
		}
		if difficulty != "" && strings.ToLower(t.Difficulty) != difficulty {
			continue
		}
		filtered = append(filtered, t)
	}
	return filtered
}

// Categories returns the unique category names in the task list.
func Categories(tasks []*Task) []string {
	seen := make(map[string]bool)
	var cats []string
	for _, t := range tasks {
		if !seen[t.Category] {
			seen[t.Category] = true
			cats = append(cats, t.Category)
		}
	}
	sort.Strings(cats)
	return cats
}
