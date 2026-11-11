# Discord notifications

FiveMesh Backup can post to a Discord channel when:

| Event | Category | Example |
|---|---|---|
| A backup succeeds | `success` | size, duration, archive name, retention cleanup |
| A backup fails or is interrupted | `failure` | the step that failed, the error, and the command to read the full log |
| A restore finishes or fails | `restore` | target database, archive, duration |
| Settings change | `config` | profile created or updated, schedule configured, paused, or removed, uninstall |

Scheduled backups run the same `backup` command as manual ones, so they send
the same messages.

## Set up

1. In Discord, open **Server Settings → Integrations → Webhooks → New
   Webhook**, choose the channel, and **Copy Webhook URL**.
2. Run:

   ```sh
   fivemesh-backup notify discord
   ```

   Paste the URL (it is not shown on screen), choose which events to post,
   and give this server a name. A test message is sent before anything is
   saved, so a wrong URL is caught immediately.

Guided setup (`fivemesh-backup setup`) offers the same step the first time.

For scripts and provisioning:

```sh
printf '%s\n' "$WEBHOOK_URL" | fivemesh-backup notify discord \
  --webhook-url-stdin --events failure,config --server-name eu-1
```

`--events` takes `all` or a comma list of `success`, `failure`, `restore`, and
`config`. `--mention "<@&ROLE_ID>"` pings a role (or `<@USER_ID>` a user) on
failures only; `--mention none` clears it. `--no-test` skips the test message.

## Manage

```sh
fivemesh-backup notify status    # settings, webhook shown masked
fivemesh-backup notify test      # send a test message
fivemesh-backup notify disable   # pause without forgetting the webhook
fivemesh-backup notify enable
fivemesh-backup notify remove    # forget the webhook and settings
```

Running `notify discord` again changes the settings; press Enter at the URL
prompt to keep the current webhook.

## How it behaves

- **Best effort.** A notification never changes the result of a backup,
  restore, or settings change. If Discord cannot be reached, the command
  prints a warning and carries on. Delivery is limited to about 15 seconds,
  and Discord's rate limits are respected with one short retry.
- **No surprise pings.** Messages never mention `@everyone`, `@here`, roles,
  or users, even if an error message contains them. The only mention ever sent
  is the one you configure for failures.
- **Every machine labels itself.** The server name (the hostname by default)
  appears in each message, so several servers can share one channel.
- **No message if a backup never starts.** If the machine is off or the
  scheduled task cannot start at all, nothing runs to send a failure. Use
  `fivemesh-backup schedule status` to check the last run.

## Storage and security

The webhook URL is a secret: anyone who has it can post to your channel. It is
stored in `credentials.toml` next to the database passwords, with the same
protection (mode `0600` on Linux, a restricted ACL on Windows), and is never
written to `config.toml`, logs, history, or error messages. `notify status`
shows it masked.

If a webhook leaks, delete it in Discord and run `fivemesh-backup notify
discord` with a new one.

The non-secret settings live in `config.toml`:

```toml
[notifications.discord]
enabled = true
events = ["failure", "config"]
server_name = "eu-1"
mention_on_failure = "<@&123456789012345678>"
```
