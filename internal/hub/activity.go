package hub

import "github.com/Molten-Bot/moltenhub-code/internal/config"

const (
	libraryTaskActivityStartPrefix    = "working on library task: "
	libraryTaskActivityCompletePrefix = "completed library task: "
)

// RunStartedActivity returns the custom task-start activity for runCfg.
func RunStartedActivity(runCfg config.Config) string {
	return libraryTaskStartActivity(runCfg)
}

// RunCompletedActivity returns the custom task-complete activity for runCfg.
func RunCompletedActivity(runCfg config.Config) string {
	return libraryTaskCompleteActivity(runCfg)
}

func libraryTaskStartActivity(runCfg config.Config) string {
	return buildLibraryTaskActivity(libraryTaskActivityStartPrefix, libraryTaskActivityName(runCfg))
}

func libraryTaskCompleteActivity(runCfg config.Config) string {
	return buildLibraryTaskActivity(libraryTaskActivityCompletePrefix, libraryTaskActivityName(runCfg))
}

func libraryTaskActivityName(runCfg config.Config) string {
	taskName := normalizeActivityEntry(runCfg.LibraryTaskName)
	if taskName == "" {
		return ""
	}
	if displayName := normalizeActivityEntry(runCfg.LibraryTaskDisplayName); displayName != "" {
		return displayName
	}
	return taskName
}

func buildLibraryTaskActivity(prefix, taskName string) string {
	normalizedTaskName := normalizeActivityEntry(taskName)
	if normalizedTaskName == "" {
		return ""
	}
	return prefix + normalizedTaskName
}
