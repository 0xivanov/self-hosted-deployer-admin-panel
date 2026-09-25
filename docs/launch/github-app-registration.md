# Launchstead GitHub App registration

Prepared September 25, 2026. Registration is not complete. The authenticated
@0xivanov account currently has no owned GitHub Apps; GitHub requires a passkey or
authenticator check before opening the registration form.

## Registration settings

| Field | Value |
|---|---|
| App name | Launchstead Deploy (use Launchstead Deploy by 0xivanov if unavailable) |
| Description | Deploy selected static and Node.js repositories to Launchstead hosting. |
| Homepage | https://launchstead.0xivanov.dev |
| User authorization callback | https://portal.0xivanov.dev/github/callback |
| Request user authorization during installation | Off; authorization starts from the signed-in portal project |
| Device flow | Off |
| Setup URL | https://portal.0xivanov.dev/ |
| Webhook URL | https://portal.0xivanov.dev/webhooks/github |
| Webhook active | On after the configured endpoint is deployed |
| SSL verification | On |
| Repository Contents | Read-only |
| Repository Metadata | Read-only (GitHub-required default) |
| Other repository, organization and account permissions | None |
| Events | Push |
| Installation availability | Any account, to support invited customers installing on their own repositories |

Installation must select individual repositories. Start with one disposable
qualification repository containing a supported static site, then a supported
Node.js source fixture. Never install on all repositories by default. App creation
and installation are separate operations; registration alone does not prove access.

The portal creates its own expiring authorization state and PKCE challenge. An
installation-driven OAuth callback without that state cannot complete linking,
which is why authorization during installation is disabled.

GitHub's registration UI must receive a random webhook secret of at least 32 bytes.
Keep it in private operator configuration, not a URL, issue, screenshot or repository.
The runtime config requires `client_id`, `client_secret`, `private_key_pem` and
`webhook_secret` in the private JSON file passed to `--github-config`. Use the App's
own client ID/secret and generated RSA private key. Do not use a personal access
token. Do not record secret values in this document.

## Activation sequence

1. Complete GitHub's account verification, register the App and obtain its private
   configuration. Review read-only permissions before installation.
2. Back up the production portal database, installed consumer binaries and config.
   Drain writers and upgrade every portal database consumer together to schema 53.
3. Configure the App and webhook secret while automatic deployment remains off.
   Verify a signed provider ping and owner connection to the selected repository.
4. Verify a real manual import, its source revision, and the normal publish path.
5. Enable `--github-auto-deploy`, enable the project control and verify a real push,
   update, failure recovery and disconnect on the assigned runtime. Keep existing
   customer websites intact. The fleet is currently at its six-project admission
   capacity, so do not delete an existing project to make room for qualification.
6. Update rollout evidence and product copy only after these real checks pass.

Read-only observation at 11:50 UTC September 25: all three nodes Ready, all eight
application Deployments available, core schema 12 and portal schema 46 healthy,
no unfinished deployment/build/publication work. Six projects, eight uploads,
three static publication pointers and four Node releases remain present. This
observation is not a backup and must be refreshed before an actual upgrade.

Sources: [GitHub registration](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/registering-a-github-app),
[registration parameters](https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-using-url-parameters),
[push event permissions](https://docs.github.com/en/webhooks/webhook-events-and-payloads#push).
