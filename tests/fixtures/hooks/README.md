# Hook fixtures

`payloads/<agent>.<Event>.json` are the hook payloads the fake agents (`tests/fakes/agents`) send
through the real relay, with `__SESSION__`, `__CWD__` and `__PROMPT__` filled in. They follow each
agent's hook documentation; they were not captured from a running agent.

`settings/` holds inputs and expected outputs of the hook installers (`internal/hooks`).
