## Workflow

- After a significant amount of work, commit the changes. If several changes were applied, commit them in such a way each commit represents a feature or unit of work.
- Write unit tests for anything that can have unit tests. Run tests before committing.
- Run `go install` alongside the commit at the end of every unit of work, so the binary on this machine stays current after every change.
