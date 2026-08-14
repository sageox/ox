# Private/server-side follow-up

When a public `ox` issue, diagnostic, or fix identifies work that belongs to
`sageox-monorepo` or another private server-side repository, create a private
GitHub issue in the owning repository if no suitable private issue already
tracks it.

- Keep the private issue scoped to the owner work and its private tests.
- The public repository may state only that private-owner follow-up is needed.
  Do not link to, quote, summarize, copy, or expose a private issue number,
  title, implementation detail, path, API, or remediation plan.
- Do not inspect private source merely to enrich a public change. Work in a
  private repository requires explicit user authorization and must remain
  private-only.
- Public pull requests must accurately distinguish their shipped public scope
  from outstanding private-owner follow-up; never close an umbrella issue until
  both scopes are actually complete.
