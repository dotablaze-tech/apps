#!/usr/bin/env bash
# Authorizes clipbot for clips:edit and writes every secret to Vault in one go.
# Run it in your own terminal, never through an agent: it handles the client
# secret and mints a refresh token, and nothing here prints either of them.
#
# Usage: TWITCH_CLIENT_ID=<id> TWITCH_BROADCASTER_ID=<id> ./seed-vault.sh
# Needs: curl, jq, and a logged-in vault CLI (VAULT_ADDR set).
set -euo pipefail

: "${TWITCH_CLIENT_ID:?set TWITCH_CLIENT_ID}"
: "${TWITCH_BROADCASTER_ID:?set TWITCH_BROADCASTER_ID}"
VAULT_PATH="${VAULT_PATH:-kv/dotablaze-tech-clipbot}"
# The redirect never has to load: the code is read from the address bar.
REDIRECT_URI="http://localhost:3000/callback"

vault token lookup >/dev/null || { echo "vault is not logged in" >&2; exit 1; }

read -rsp "Twitch client secret: " client_secret
echo
state=$(openssl rand -hex 16)
echo "Open this URL, signed in as the broadcaster, and approve:"
echo "https://id.twitch.tv/oauth2/authorize?response_type=code&client_id=${TWITCH_CLIENT_ID}&redirect_uri=${REDIRECT_URI}&scope=clips:edit&state=${state}"
echo "The browser then fails to load localhost; paste that full address here."
read -rsp "Redirected URL: " redirected
echo

[[ "$redirected" == *"state=${state}"* ]] || { echo "state mismatch; start again" >&2; exit 1; }
code=$(sed -n 's/.*[?&]code=\([^&]*\).*/\1/p' <<<"$redirected")
[ -n "$code" ] || { echo "no code in that URL" >&2; exit 1; }

token_json=$(curl -sf -X POST https://id.twitch.tv/oauth2/token \
  --data-urlencode "client_id=${TWITCH_CLIENT_ID}" \
  --data-urlencode "client_secret=${client_secret}" \
  --data-urlencode "code=${code}" \
  --data-urlencode "grant_type=authorization_code" \
  --data-urlencode "redirect_uri=${REDIRECT_URI}") || { echo "token exchange failed" >&2; exit 1; }
refresh_token=$(jq -r '.refresh_token // empty' <<<"$token_json")
scopes=$(jq -r '.scope | join(" ")' <<<"$token_json")
[ -n "$refresh_token" ] || { echo "no refresh token returned" >&2; exit 1; }
[ "$scopes" = "clips:edit" ] || { echo "unexpected scopes: $scopes" >&2; exit 1; }

# Values go through stdin so they never appear in the process list.
jq -n --arg ck "$(openssl rand -hex 24)" --arg ci "$TWITCH_CLIENT_ID" --arg cs "$client_secret" \
  --arg rt "$refresh_token" --arg bi "$TWITCH_BROADCASTER_ID" \
  '{clip_key:$ck, client_id:$ci, client_secret:$cs, refresh_token:$rt, broadcaster_id:$bi}' |
  vault kv put "$VAULT_PATH" - >/dev/null
echo "Wrote clip_key, client_id, client_secret, refresh_token, broadcaster_id to ${VAULT_PATH}."
echo "Read the key for the StreamElements command with: vault kv get -field=clip_key ${VAULT_PATH}"
