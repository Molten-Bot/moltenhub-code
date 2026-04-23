package hub

import "github.com/Molten-Bot/moltenhub-code/internal/config"

const (
	libraryTaskActivityStartPrefix    = "working on library task: "
	libraryTaskActivityCompletePrefix = "completed library task: "
)

// RunStartedActivity returns the custom task-start activity for runCfg.
func RunStartedActivity(runCfg config.Config) string {
	return libraryTaskStartActivity(runCfg.LibraryTaskName)
}

// RunCompletedActivity returns the custom task-complete activity for runCfg.
func RunCompletedActivity(runCfg config.Config) string {
	return libraryTaskCompleteActivity(runCfg.LibraryTaskName)
}

func libraryTaskStartActivity(taskName string) string {
	return buildLibraryTaskActivity(libraryTaskActivityStartPrefix, taskName)
}

func libraryTaskCompleteActivity(taskName string) string {
	return buildLibraryTaskActivity(libraryTaskActivityCompletePrefix, taskName)
}

func buildLibraryTaskActivity(prefix, taskName string) string {
	normalizedTaskName := normalizeActivityEntry(taskName)
	if normalizedTaskName == "" {
		return ""
	}
	return prefix + normalizedTaskName
}
