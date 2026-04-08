package failurefollowup

const RequiredPrompt = "Review the failing log paths first, identify every root cause behind the failed task, fix the underlying issues in this repository, validate locally where possible, and summarize the verified results."

const ExecutionContract = `Issue an offline to moltenbot hub -> review na.hub.molten.bot.openapi.yaml for integration behaviours.

When failures occur, send a response back to the calling agent that clearly states failure and includes the error details.

When a task fails:
- Queue a follow-up task dedicated to reviewing the logs and fixing all underlying issues in this codebase.
- Pass the relevant failing file/folder log path(s) into that follow-up task context.
- Use this follow-up run config payload shape:
  {"repos":["<same_repo_as_failed_task>"],"baseBranch":"main","targetSubdir":".","prompt":"Review the failing log paths first, identify every root cause behind the failed task, fix the underlying issues in this repository, validate locally where possible, and summarize the verified results."}

Completion requirements:
- Only create a new branch when starting from 'main'; if you're fixing an existing non-'main' branch, stay on it.
- Start every new branch and every PR title with 'moltenhub-'.
- Keep working until there is a PR for your changes and required CI/CD checks are green, unless you can verify the request is already satisfied with no required code/workflow/docs changes.
- If no file changes are required, return a clear no-op result with concrete evidence instead of forcing an empty PR.
- If CI/CD fails, continue fixing code/tests/workflows until checks pass.
- If you changed multiple repositories, ensure each changed repository has its own branch and PR.
- Optimize for the highest-quality PR you can produce with focused, production-ready changes.`
