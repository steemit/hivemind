# Manual Deploy Runbook (Elastic Beanstalk)

Deploys are executed manually by the operator (decision 2026-10-16); there is
no automated `deploy-eb` CI job. The image pipeline (`images` job in
`.github/workflows/build.yml`) builds `steemit/hivemind:server-<sha>` /
`:indexer-<sha>` on every push to `next`/`master` — pushes to the registry
activate automatically once the `DOCKERHUB_USERNAME` / `DOCKERHUB_TOKEN` repo
secrets are configured (until then PR builds are local-only validations).

## Deploy a commit to EB

Prerequisites: `eb` CLI configured, AWS credentials with EB write access for
the target environment.

```bash
# 1. Pick the commit and note its sha (must be an image that was pushed).
SHA=<commit-sha>

# 2. Point Dockerrun.aws.json at that image.
sed -i "s|steemit/hivemind:server-[^\"']*|steemit/hivemind:server-${SHA}|" Dockerrun.aws.json

# 3. Apply environment values (RDS URL / OTLP endpoint) if not already set
#    on the EB environment:
#      eb setenv HIVE_DATABASE_URL=... HIVE_TRACES_ENDPOINT=...

# 4. Deploy.
eb deploy <environment-name>
```

## Reference

- Environment values and health-check settings: `.ebextensions/01-hivemind.config`
- Catch-up grace notes (head-lag health during indexer catch-up):
  `05-deploy` plan doc section 2.5 — set `HIVE_HEALTH_MAX_HEAD_AGE=0` on the
  reader during long catch-up windows or raise the EB HealthGracePeriod.
