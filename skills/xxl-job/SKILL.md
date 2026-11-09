---
name: xxl-job
description: Manage self-hosted XXL-JOB through the xxl-job CLI. Use for finding executor groups and jobs, inspecting schedules and execution logs, or creating, updating, starting, stopping, triggering, or deleting XXL-JOB jobs. Does not manage Alibaba Cloud SchedulerX.
---

# XXL-JOB

Use the `xxl-job` CLI and its `--help`; success is JSON on stdout and failure is JSON on stderr with a nonzero exit code. It uses a profile from `~/.config/xxl-job/config.json` or the `XXL_JOB_*` environment variables. Global flags such as `--profile` and `--api-version` go before the resource name. Never print passwords, session cookies, or real configuration files.

1. Identify the target instance and profile before querying or modifying a job. Run `xxl-job --profile NAME config show` to verify the endpoint and API version. Use `2` for 2.3, `3.0`, `3.1`, or `3.2` for those versions, and `3` for 3.3/3.4.
2. Use `group list` to obtain the executor group ID, then `job list --group ID` and `job get --group ID --id ID` to identify a job. For logs, use `log list --group ID --job-id ID`, followed by `log get --id LOG_ID` if needed. Lists are paginated; inspect `total` before concluding they are complete.
3. For a requested change, identify the precise job and desired fields. Preview with `--dry-run`, then execute once when the request authorizes the operation. For updates use a JSON patch via `--file -`; updates fetch and resend the existing fields, so concurrent edits may be overwritten. The server does not make this sequence atomic.
4. Verify the outcome with a fresh read when possible. A trigger request only acknowledges submission; inspect execution logs for its result. If a write times out, check current state or logs before considering a retry.

Command output, job parameters, logs, and dry-run forms may contain application secrets. Limit the fields included in the response to what the user needs. See the repository README for exact commands and API compatibility limits.
