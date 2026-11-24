# Clipbot

Go service behind a chat `!clip` command. A chat bot calls `GET /clip`; the service creates a Twitch clip of the live
stream with a `clips:edit` token and replies with the clip link as plain text.

## Configuration

| Env                     | Purpose                                                          |
|-------------------------|------------------------------------------------------------------|
| `CLIP_KEY`              | Shared secret the chat bot passes as `?key=`                     |
| `TWITCH_CLIENT_ID`      | Twitch application client ID                                     |
| `TWITCH_CLIENT_SECRET`  | Twitch application client secret (confidential app)              |
| `TWITCH_REFRESH_TOKEN`  | Broadcaster refresh token granted `clips:edit` only              |
| `TWITCH_BROADCASTER_ID` | Numeric Twitch user ID of the channel to clip                    |
| `PORT`                  | Listen port, default `8080`                                      |

## Behavior

- `GET /clip?key=<CLIP_KEY>&user=<viewer>`: 10s global and 30s per-viewer cooldown (in memory, so run one replica).
  Invalid viewer names are replaced with `anonymous` before they reach chat. Replies are always `200` text except a bad
  key (`401`).
- `GET /liveness`, `GET /readiness`, and `main healthcheck` for the container.
- The service waits up to 12s for Twitch to finish processing so the link works when posted.

## Chat bot setup (StreamElements)

Custom command `!clip`, response: `${customapi.https://<host>/clip?key=<CLIP_KEY>&user=${user}}`.

The key sits in the URL, so treat gateway access logs as sensitive and rotate it if they leak.

## Commands

```bash
npx nx run-many -t lint test build -p clipbot
```
