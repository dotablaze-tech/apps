#!/usr/bin/env bash
# Authorizes clipbot for clips:edit and writes every secret to Vault in one go.
# Run it in your own terminal, never through an agent: it handles the client
# secret and mints a refresh token, and nothing here prints either of them.
#
# Usage: TWITCH_CLIENT_ID=<id> TWITCH_BROADCASTER_ID=<id> ./seed-vault.sh
# Needs: curl, jq, python3 and a Vault token (it runs `vault login` if needed).
set -euo pipefail

: "${TWITCH_CLIENT_ID:?set TWITCH_CLIENT_ID}"
: "${TWITCH_BROADCASTER_ID:?set TWITCH_BROADCASTER_ID}"
VAULT_PATH="${VAULT_PATH:-kv/dotablaze-tech-clipbot}"
export VAULT_ADDR="${VAULT_ADDR:-https://vault.jdwlabs.com}"
# The redirect never has to load: the code is read from the address bar.
REDIRECT_URI="http://localhost:3000/callback"

vault token lookup >/dev/null 2>&1 || vault login -no-print || { echo "vault login failed" >&2; exit 1; }

read -rsp "Twitch client secret: " client_secret
echo
state=$(openssl rand -hex 16)
auth_url="https://id.twitch.tv/oauth2/authorize?response_type=code&client_id=${TWITCH_CLIENT_ID}&redirect_uri=${REDIRECT_URI}&scope=clips:edit&state=${state}"
# Twitch redirects the browser to localhost:3000; this one-shot listener reads
# the code from that request, so nothing has to be copied by hand. It only
# works when the browser runs on this machine.
listener() {
  python3 -I -c '
import http.server, sys, urllib.parse
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        q = urllib.parse.parse_qs(urllib.parse.urlparse(self.path).query)
        self.send_response(200); self.end_headers()
        self.wfile.write(b"clipbot authorized, you can close this tab.")
        sys.stdout.write(q.get("state", [""])[0] + " " + q.get("code", [""])[0]); sys.stdout.flush()
    def log_message(self, *a): pass
http.server.HTTPServer(("127.0.0.1", 3000), H).handle_request()'
}
coproc LISTEN { listener; }
echo "Approve clipbot in the browser (signed in as the broadcaster):"
echo "$auth_url"
# Open it in the shared agent browser when one is running; harmless otherwise.
curl -sf -X PUT "http://127.0.0.1:9222/json/new?$(jq -rn --arg u "$auth_url" '$u|@uri')" >/dev/null 2>&1 || true
read -r got_state code <&"${LISTEN[0]}"
[ "$got_state" = "$state" ] || { echo "state mismatch; start again" >&2; exit 1; }
[ -n "$code" ] || { echo "no code returned (did you click Authorize?)" >&2; exit 1; }

# Secrets travel by env and stdin, never argv, so the process list cannot show them.
token_json=$(CS="$client_secret" jq -rn --arg ci "$TWITCH_CLIENT_ID" --arg c "$code" --arg r "$REDIRECT_URI" \
  '"client_id=\($ci|@uri)&client_secret=\(env.CS|@uri)&code=\($c|@uri)&grant_type=authorization_code&redirect_uri=\($r|@uri)"' |
  curl -sf -X POST https://id.twitch.tv/oauth2/token --data-binary @-) || { echo "token exchange failed" >&2; exit 1; }
refresh_token=$(jq -r '.refresh_token // empty' <<<"$token_json")
scopes=$(jq -r '.scope | join(" ")' <<<"$token_json")
[ -n "$refresh_token" ] || { echo "no refresh token returned" >&2; exit 1; }
[ "$scopes" = "clips:edit" ] || { echo "unexpected scopes: $scopes" >&2; exit 1; }

CK="$(openssl rand -hex 24)" CS="$client_secret" RT="$refresh_token" jq -n --arg ci "$TWITCH_CLIENT_ID" --arg bi "$TWITCH_BROADCASTER_ID" \
  '{clip_key:env.CK, client_id:$ci, client_secret:env.CS, refresh_token:env.RT, broadcaster_id:$bi}' |
  vault kv put "$VAULT_PATH" - >/dev/null
echo "Wrote clip_key, client_id, client_secret, refresh_token, broadcaster_id to ${VAULT_PATH}."
echo "Read the key for the StreamElements command with: vault kv get -field=clip_key ${VAULT_PATH}"
