# GitHub response fixtures

What GitHub's REST API answered for the calls the hub's updater makes (`internal/update`):
the deployments of an environment, the statuses of a deployment, the artifacts of a
repository. Recorded with `gh api repos/<owner>/devbox/…` and anonymized (owner, node ids,
user ids). The fake GitHub (`tests/fakes/github`) is tested to answer with fields of the same
names and kinds (`TestTheFakeAnswersAsGitHubWasRecordedToAnswer`).
