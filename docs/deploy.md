# Deploying Vellatry

| Part | Where | Notes |
| --- | --- | --- |
| Dashboard (`web/`) | Vercel | Root Directory `web` |
| api and worker | Fly.io, Sydney (`syd`) | One image, two process groups; `migrate` runs before each release |
| Postgres | Neon, AWS Sydney (`ap-southeast-2`), Postgres 17 | Postgres 16+ is required (`WITH INHERIT FALSE`) |
| Report PDFs (optional) | Gotenberg on Fly, private network only | Without it, reports have a web view only |
| Raw Search Console / GA4 facts | BigQuery, `australia-southeast1` | Needed before Google connections are switched on |

Everything customer-facing runs in Sydney: the customers are Australian teams, and
their data stays in Australia.

The api and worker are one binary (`cmd/vellatry`) and one image (`Dockerfile`).
`fly.toml` runs it three ways: `migrate` as the release command, then `api` (the only
process with a public address) and `worker` (no address; it runs the job queue).

## Safety checks you will meet

- **The database user must not bypass row-level security.** `db.Open` refuses a
  superuser or a `BYPASSRLS` role on every start. Providers' default admin users are
  often exactly that, so pasting the connection string they show you fails with
  `ErrBypassesRLS`. Connect as `vellatry_app` (step 1).
- **`VELLATRY_ENV=production`** (set in `fly.toml`) refuses header sign-in
  (`VELLATRY_DEV_AUTH`), a missing `VELLATRY_SECRET_KEY`, localhost URLs, and Google
  connections without BigQuery. It lists every problem at once.
  `internal/config` tests `fly.toml` against these checks, so a bad value there fails CI.

## 1. Postgres on Neon

1. Create a Neon project in **AWS Asia Pacific (Sydney)** with Postgres 17.
2. Create the app role and database. Connect with the admin connection string Neon
   shows you (user `neondb_owner`), once:

   ```bash
   psql "<neondb_owner connection string>" -v ON_ERROR_STOP=1 -v app_password="<new password>" -f deploy/postgres-bootstrap.sql
   ```

   Generate the password with `openssl rand -hex 24` (hex, so it needs no escaping in a
   URL) and keep it in your password manager. Without `psql`, run the two statements in
   Neon's SQL Editor one at a time, with the password in place of `:'app_password'`.
3. The app's `DATABASE_URL` uses the **direct** host (the one *without* `-pooler`),
   because the job queue uses `LISTEN`, which a transaction pooler does not carry:

   ```
   postgres://vellatry_app:<password>@<endpoint>.ap-southeast-2.aws.neon.tech/vellatry?sslmode=require
   ```

The worker polls its queue, so the database never scales to zero. Check that your Neon
plan covers a compute running around the clock.

## 2. Sign-in (Clerk)

The dashboard signs people in with Clerk (`/sign-in`, `/sign-up`), and the api checks
every request's Clerk token. The api will not start in production without it.

1. Create a Clerk application. Choose the sign-in methods you want (email, Google).
2. **Put the email in the session token.** Clerk → Sessions → Customize session token:

   ```json
   { "email": "{{user.primary_email_address}}" }
   ```

   The api identifies people by this claim. Without it, users get a placeholder
   address, and anything keyed on email breaks: team membership for the CMO hub,
   email alerts. It corrects itself on the next sign-in once the claim is added.
3. Note three values:
   - `CLERK_ISSUER` (api): the Frontend API URL, for example
     `https://<name>.clerk.accounts.dev`;
   - `CLERK_AUTHORIZED_PARTIES` (api): the dashboard's origin,
     `https://vellatry.vercel.app`;
   - `NEXT_PUBLIC_CLERK_PUBLISHABLE_KEY` (Vercel): the publishable key from API keys.

   The dashboard does no server-side auth, so it needs no Clerk secret key.

**Clerk's production mode needs a domain you own**, with DNS records Clerk gives you, so
it cannot run on `vellatry.vercel.app`. Until Vellatry has its own domain, use the
Clerk application's development instance. It works on any address, shows a small
"Development mode" badge, and is limited to a small number of users: fine for the
first design partners. When the domain arrives, create the production instance and
swap the three values.

Leave `NEXT_PUBLIC_CLERK_PUBLISHABLE_KEY` empty locally: Clerk is then not loaded at
all, and the Account page's header sign-in is used (with `VELLATRY_DEV_AUTH=1` on the
api).

## 3. The api and worker on Fly

Install `flyctl` (https://fly.io/docs/flyctl/install/), then:

```bash
fly auth login
```

```bash
fly apps create vellatry-api
```

If the name is taken, choose another, then change `app` and every
`vellatry-api.fly.dev` URL in `fly.toml` to match.

Generate the secret key, and **save a copy in your password manager before setting
it**. It seals stored Google and Asana credentials; changing it later means every
connection has to be made again.

```bash
openssl rand -base64 32
```

```bash
fly secrets set -a vellatry-api DATABASE_URL="<from step 1>" VELLATRY_SECRET_KEY="<the key>" CLERK_ISSUER="<from step 2>" CLERK_AUTHORIZED_PARTIES="https://vellatry.vercel.app"
```

The first deploy, with one machine for each process instead of Fly's default two:

```bash
fly deploy --ha=false
```

Check it:

```bash
curl https://vellatry-api.fly.dev/healthz
```

```bash
fly logs -a vellatry-api
```

The worker logs one warning per feature it switched off because its credentials are
missing. That is expected; add them as the features are needed:

| Feature | Secrets |
| --- | --- |
| AI answers and keyword research | `DATAFORSEO_LOGIN`, `DATAFORSEO_PASSWORD` (daily caps: `DATAFORSEO_DAILY_USD`, `DATAFORSEO_KEYWORDS_DAILY_USD`) |
| Report summaries, the agent's model | `ANTHROPIC_API_KEY` |
| Email (alerts, digest, CMO sign-in links) | `POSTMARK_SERVER_TOKEN`, `EMAIL_FROM` |
| Asana | `ASANA_CLIENT_ID`, `ASANA_CLIENT_SECRET` (redirect: `https://vellatry-api.fly.dev/oauth/asana/callback`) |
| Google (Search Console, GA4) | `GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET`, plus BigQuery (below) |

Setting a secret restarts the machines.

## 4. The dashboard on Vercel

1. Project → Settings → Build and Deployment → **Root Directory**: `web`.
2. Environment variables (compiled into the build, so set them before deploying):
   - `NEXT_PUBLIC_API_URL` = `https://vellatry-api.fly.dev`
   - `NEXT_PUBLIC_CLERK_PUBLISHABLE_KEY` = the key from step 2
   - `NEXT_PUBLIC_SITE_URL` = `https://vellatry.vercel.app` (canonical links and link
     previews on the landing page)
3. Redeploy.

`/` is the public landing page and the only page search engines may index. The app
itself starts at `/today` and is marked noindex.

If the dashboard moves to its own domain, update `APP_URL`, `ALLOWED_ORIGINS` and
`CLERK_AUTHORIZED_PARTIES` to match.

## 5. Report PDFs (optional)

```bash
fly apps create vellatry-gotenberg
```

```bash
fly deploy -c deploy/gotenberg/fly.toml --no-public-ips --ha=false
```

```bash
fly ips allocate-v6 --private -a vellatry-gotenberg
```

Then uncomment `GOTENBERG_URL` in `fly.toml` and deploy the api again. Gotenberg sleeps
between reports and wakes on the worker's first request.

## 6. Google connections (later)

Google needs two things in place first:
- an OAuth client whose redirect URI is `https://vellatry-api.fly.dev/oauth/google/callback`;
- BigQuery for the raw facts.

The worker authenticates to BigQuery with Application Default Credentials. Getting a
service-account credential onto the worker machine is not wired up yet. Until it is,
production refuses Google connections without `BIGQUERY_PROJECT`, rather than keeping
the facts in memory.

## Deploying changes

- **By hand:** `fly deploy`. Migrations run first, and a failed migration stops the
  release while the running version keeps serving.
- **Automatically:** `.github/workflows/deploy.yml` deploys `main` after `ci` passes,
  and deploys exactly the commit CI tested. It stays off until the repository has a
  `FLY_API_TOKEN` secret:

  ```bash
  fly tokens create deploy -a vellatry-api
  ```

  Add the token under GitHub → Settings → Secrets and variables → Actions.
