package session

import "regexp"

// gitleaks_detectors.go ports a curated subset of the gitleaks default
// rule set into ox's SecretPattern format. Source:
// https://github.com/gitleaks/gitleaks (config/gitleaks.toml), MIT
// licensed. Each rule below carries the gitleaks rule ID it descended
// from in a comment so future updates can be traced.
//
// Why port instead of import (per ox-h20u):
//
//   - The gitleaks Go module pulls in a wazero WASM runtime and other
//     heavy dependencies that ox doesn't otherwise need. The runtime
//     overhead matters because these patterns run on every raw.jsonl
//     write in the chokepoint.
//   - The rules are static data, MIT licensed, small to translate.
//   - Local control over allowlist semantics and slug naming matches
//     ox's existing detector conventions.
//
// Ongoing updates: when gitleaks ships new rules, port the high-value
// ones here in a follow-up PR. The rule IDs in comments below make
// drift checks straightforward.

// DefaultExtraDetectors returns gitleaks-derived patterns used by
// RawWriter as a third layer behind the cmd-allowlist redactor and the
// built-in DefaultPatterns. Slugs follow the [REDACTED_<CLASS>] style
// so downstream consumers can grep across both detector tiers.
func DefaultExtraDetectors() []SecretPattern {
	return []SecretPattern{
		// --- Cloud platforms ---

		// gitleaks: gcp-api-key
		{
			Name:    "gcp_api_key",
			Pattern: regexp.MustCompile(`AIza[0-9A-Za-z\-_]{35}`),
			Redact:  "[REDACTED_GCP_API_KEY]",
		},
		// gitleaks: gcp-service-account
		{
			Name:    "gcp_service_account",
			Pattern: regexp.MustCompile(`"type":\s*"service_account"`),
			Redact:  `"type": "[REDACTED_GCP_SERVICE_ACCOUNT]"`,
		},
		// gitleaks: azure-active-directory-client-secret (heuristic — keys with .Azure context)
		{
			Name:    "azure_subscription_key",
			Pattern: regexp.MustCompile(`(?i)Ocp-Apim-Subscription-Key:\s*[a-z0-9]{32}`),
			Redact:  "Ocp-Apim-Subscription-Key: [REDACTED_AZURE_SUBSCRIPTION_KEY]",
		},

		// --- AI / LLM providers ---

		// gitleaks: openai-api-key
		{
			Name:    "openai_api_key",
			Pattern: regexp.MustCompile(`sk-[A-Za-z0-9]{20,}T3BlbkFJ[A-Za-z0-9]{20,}`),
			Redact:  "[REDACTED_OPENAI_KEY]",
		},
		// gitleaks: openai-api-key (newer project-scoped sk-proj-)
		{
			Name:    "openai_project_key",
			Pattern: regexp.MustCompile(`sk-proj-[A-Za-z0-9_\-]{40,}`),
			Redact:  "[REDACTED_OPENAI_PROJECT_KEY]",
		},
		// gitleaks: anthropic-api-key
		{
			Name:    "anthropic_api_key",
			Pattern: regexp.MustCompile(`sk-ant-[A-Za-z0-9_\-]{60,}`),
			Redact:  "[REDACTED_ANTHROPIC_KEY]",
		},
		// gitleaks: cohere-api-key (heuristic; co_… pattern is common)
		{
			Name:    "cohere_api_key",
			Pattern: regexp.MustCompile(`co_[A-Za-z0-9]{40}`),
			Redact:  "[REDACTED_COHERE_KEY]",
		},

		// --- DevOps / observability ---

		// gitleaks: datadog-access-token
		{
			Name:    "datadog_api_key",
			Pattern: regexp.MustCompile(`(?i)(?:datadog|dd)[_\-]?(?:api|app)[_\-]?key[\s:=]+["']?[a-f0-9]{32}["']?`),
			Redact:  "[REDACTED_DATADOG_KEY]",
		},
		// gitleaks: pagerduty-api-key
		{
			Name:    "pagerduty_api_key",
			Pattern: regexp.MustCompile(`\b[A-Za-z0-9_+\-]{20}\b\.pdt[A-Za-z0-9]+`),
			Redact:  "[REDACTED_PAGERDUTY_KEY]",
		},
		// gitleaks: sentry-auth-token
		{
			Name:    "sentry_auth_token",
			Pattern: regexp.MustCompile(`sntrys_[A-Za-z0-9+/=_\-]{40,}`),
			Redact:  "[REDACTED_SENTRY_TOKEN]",
		},
		// gitleaks: new-relic-user-api-key
		{
			Name:    "newrelic_user_key",
			Pattern: regexp.MustCompile(`NRAK-[A-Z0-9]{27}`),
			Redact:  "[REDACTED_NEWRELIC_USER_KEY]",
		},
		// gitleaks: new-relic-ingest-browser-api-token
		{
			Name:    "newrelic_ingest_token",
			Pattern: regexp.MustCompile(`NRBR-[A-Z0-9]{27}`),
			Redact:  "[REDACTED_NEWRELIC_INGEST_TOKEN]",
		},
		// gitleaks: grafana-cloud-api-token
		{
			Name:    "grafana_cloud_token",
			Pattern: regexp.MustCompile(`glc_[A-Za-z0-9+/=]{32,}`),
			Redact:  "[REDACTED_GRAFANA_CLOUD_TOKEN]",
		},
		// gitleaks: grafana-service-account-token
		{
			Name:    "grafana_service_account_token",
			Pattern: regexp.MustCompile(`glsa_[A-Za-z0-9]{32}_[a-f0-9]{8}`),
			Redact:  "[REDACTED_GRAFANA_SA_TOKEN]",
		},

		// --- Secret stores / vault ---

		// gitleaks: hashicorp-vault (Vault root token shape)
		{
			Name:    "vault_root_token",
			Pattern: regexp.MustCompile(`hvs\.[A-Za-z0-9_\-]{90,}`),
			Redact:  "[REDACTED_VAULT_SERVICE_TOKEN]",
		},
		// gitleaks: doppler-api-token
		{
			Name:    "doppler_api_token",
			Pattern: regexp.MustCompile(`dp\.pt\.[A-Za-z0-9]{40,}`),
			Redact:  "[REDACTED_DOPPLER_TOKEN]",
		},

		// --- Payment processors / commerce ---

		// gitleaks: paypal-braintree-access-token
		{
			Name:    "braintree_access_token",
			Pattern: regexp.MustCompile(`access_token\$production\$[0-9a-z]{16}\$[0-9a-f]{32}`),
			Redact:  "[REDACTED_BRAINTREE_TOKEN]",
		},
		// gitleaks: square-access-token
		{
			Name:    "square_access_token",
			Pattern: regexp.MustCompile(`(?:sq0[a-z]{3}-|EAAA[A-Za-z0-9_\-]{60})[A-Za-z0-9_\-]{20,}`),
			Redact:  "[REDACTED_SQUARE_TOKEN]",
		},
		// gitleaks: shopify-private-app-access-token
		{
			Name:    "shopify_token",
			Pattern: regexp.MustCompile(`shp(?:at|ca|pa|ss)_[a-fA-F0-9]{32}`),
			Redact:  "[REDACTED_SHOPIFY_TOKEN]",
		},

		// --- Auth / identity providers ---

		// gitleaks: auth0-client-secret-key (heuristic; Auth0 keys are 64+ chars after Auth0 hint)
		{
			Name:    "auth0_client_secret",
			Pattern: regexp.MustCompile(`(?i)auth0[_\-]?(?:client[_\-]?)?secret[\s:=]+["']?[A-Za-z0-9_\-]{40,}["']?`),
			Redact:  "[REDACTED_AUTH0_SECRET]",
		},
		// gitleaks: clerk-secret-key
		{
			Name:    "clerk_secret_key",
			Pattern: regexp.MustCompile(`sk_(?:test|live)_[A-Za-z0-9]{40,}`),
			Redact:  "[REDACTED_CLERK_SECRET]",
		},
		// gitleaks: supabase-service-role-jwt (heuristic — JWTs are caught by jwt_token already)

		// --- Communication / messaging ---

		// gitleaks: slack-webhook-url
		{
			Name:    "slack_webhook_url",
			Pattern: regexp.MustCompile(`https://hooks\.slack\.com/services/T[A-Z0-9]{8,11}/B[A-Z0-9]{8,11}/[A-Za-z0-9]{24}`),
			Redact:  "[REDACTED_SLACK_WEBHOOK]",
		},
		// gitleaks: discord-bot-token
		{
			Name:    "discord_bot_token",
			Pattern: regexp.MustCompile(`[MN][A-Za-z\d]{23}\.[\w-]{6}\.[\w-]{27}`),
			Redact:  "[REDACTED_DISCORD_BOT_TOKEN]",
		},
		// gitleaks: discord-webhook-url
		{
			Name:    "discord_webhook_url",
			Pattern: regexp.MustCompile(`https://(?:canary\.|ptb\.)?discord(?:app)?\.com/api/webhooks/\d+/[\w-]+`),
			Redact:  "[REDACTED_DISCORD_WEBHOOK]",
		},
		// gitleaks: linear-api-key
		{
			Name:    "linear_api_key",
			Pattern: regexp.MustCompile(`lin_api_[A-Za-z0-9]{40,}`),
			Redact:  "[REDACTED_LINEAR_KEY]",
		},
		// gitleaks: notion-integration-token
		{
			Name:    "notion_integration_token",
			Pattern: regexp.MustCompile(`(?:secret_|ntn_)[A-Za-z0-9]{43,}`),
			Redact:  "[REDACTED_NOTION_TOKEN]",
		},
		// gitleaks: postman-api-token
		{
			Name:    "postman_api_token",
			Pattern: regexp.MustCompile(`PMAK-[a-f0-9]{24}-[a-f0-9]{34}`),
			Redact:  "[REDACTED_POSTMAN_TOKEN]",
		},

		// --- Misc developer tools ---

		// gitleaks: cloudflare-api-key (heuristic)
		{
			Name:    "cloudflare_api_token",
			Pattern: regexp.MustCompile(`(?i)cloudflare[_\-]?(?:api[_\-]?)?(?:token|key)[\s:=]+["']?[A-Za-z0-9_\-]{40}["']?`),
			Redact:  "[REDACTED_CLOUDFLARE_TOKEN]",
		},
		// gitleaks: heroku-api-key (already covered by generic UUID; keeping explicit prefix variant)
		{
			Name:    "heroku_api_key",
			Pattern: regexp.MustCompile(`(?i)heroku[_\-]?(?:api[_\-]?)?key[\s:=]+["']?[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}["']?`),
			Redact:  "[REDACTED_HEROKU_KEY]",
		},
		// gitleaks: digitalocean-pat
		{
			Name:    "digitalocean_pat",
			Pattern: regexp.MustCompile(`dop_v1_[a-f0-9]{64}`),
			Redact:  "[REDACTED_DIGITALOCEAN_PAT]",
		},
		// gitleaks: dynatrace-api-token
		{
			Name:    "dynatrace_api_token",
			Pattern: regexp.MustCompile(`dt0[a-zA-Z]{1}[0-9]{2}\.[A-Z0-9]{24}\.[A-Z0-9]{64}`),
			Redact:  "[REDACTED_DYNATRACE_TOKEN]",
		},
		// gitleaks: snyk-api-token (UUIDv4 with snyk context)
		{
			Name:    "snyk_api_token",
			Pattern: regexp.MustCompile(`(?i)snyk[_\-]?(?:api[_\-]?)?(?:token|key)[\s:=]+["']?[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}["']?`),
			Redact:  "[REDACTED_SNYK_TOKEN]",
		},
		// gitleaks: cloudinary-credentials
		{
			Name:    "cloudinary_url",
			Pattern: regexp.MustCompile(`cloudinary://[0-9]{6,}:[A-Za-z0-9_\-]{27}@[a-z0-9_\-]+`),
			Redact:  "[REDACTED_CLOUDINARY_URL]",
		},
		// gitleaks: facebook-access-token (long-lived shape)
		{
			Name:    "facebook_access_token",
			Pattern: regexp.MustCompile(`EAACEdEose0cBA[0-9A-Za-z]+`),
			Redact:  "[REDACTED_FACEBOOK_TOKEN]",
		},
		// gitleaks: dropbox-api-token
		{
			Name:    "dropbox_token",
			Pattern: regexp.MustCompile(`(?:sl\.[A-Za-z0-9_\-]{40,}|dbx[a-z]{1,3}_[A-Za-z0-9_\-]{30,})`),
			Redact:  "[REDACTED_DROPBOX_TOKEN]",
		},
		// gitleaks: airtable-api-key
		{
			Name:    "airtable_api_key",
			Pattern: regexp.MustCompile(`pat[A-Za-z0-9]{14}\.[a-f0-9]{64}`),
			Redact:  "[REDACTED_AIRTABLE_KEY]",
		},
		// gitleaks: vercel-token
		{
			Name:    "vercel_token",
			Pattern: regexp.MustCompile(`(?i)vercel[_\-]?(?:api[_\-]?)?(?:token|key)[\s:=]+["']?[A-Za-z0-9]{24}["']?`),
			Redact:  "[REDACTED_VERCEL_TOKEN]",
		},
		// gitleaks: netlify-access-token
		{
			Name:    "netlify_token",
			Pattern: regexp.MustCompile(`(?i)netlify[_\-]?(?:api[_\-]?)?(?:token|key)[\s:=]+["']?[A-Za-z0-9_\-]{40,}["']?`),
			Redact:  "[REDACTED_NETLIFY_TOKEN]",
		},

		// --- Database / cache providers ---

		// gitleaks: planetscale-password
		{
			Name:    "planetscale_password",
			Pattern: regexp.MustCompile(`pscale_pw_[A-Za-z0-9_\-\.]{40,}`),
			Redact:  "[REDACTED_PLANETSCALE_PASSWORD]",
		},
		// gitleaks: planetscale-api-token
		{
			Name:    "planetscale_api_token",
			Pattern: regexp.MustCompile(`pscale_tkn_[A-Za-z0-9_\-\.]{40,}`),
			Redact:  "[REDACTED_PLANETSCALE_TOKEN]",
		},
		// gitleaks: mongodb-connection-string with credentials (already partially caught by connection_string)
		{
			Name:    "mongodb_srv_connection",
			Pattern: regexp.MustCompile(`mongodb\+srv://[^:]+:[^@]+@[a-z0-9\.-]+\.mongodb\.net`),
			Redact:  "[REDACTED_MONGODB_SRV]",
		},
		// gitleaks: redis-url (with auth)
		{
			Name:    "redis_url",
			Pattern: regexp.MustCompile(`redis://[^:]+:[^@]{6,}@[a-z0-9\.-]+`),
			Redact:  "[REDACTED_REDIS_URL]",
		},

		// --- Email / SMS providers ---

		// gitleaks: postmark-server-token
		{
			Name:    "postmark_server_token",
			Pattern: regexp.MustCompile(`(?i)postmark[_\-]?(?:server[_\-]?)?token[\s:=]+["']?[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}["']?`),
			Redact:  "[REDACTED_POSTMARK_TOKEN]",
		},
		// gitleaks: mailgun-api-key
		{
			Name:    "mailgun_api_key",
			Pattern: regexp.MustCompile(`key-[a-f0-9]{32}`),
			Redact:  "[REDACTED_MAILGUN_KEY]",
		},
		// gitleaks: sendinblue-api-key (now Brevo)
		{
			Name:    "brevo_api_key",
			Pattern: regexp.MustCompile(`xkeysib-[a-f0-9]{64}-[A-Za-z0-9]{16}`),
			Redact:  "[REDACTED_BREVO_KEY]",
		},

		// --- Misc package / registry ---

		// gitleaks: npm-token (already covered by npm_token in DefaultPatterns; leave for symmetry comment)

		// gitleaks: rubygems-api-key
		{
			Name:    "rubygems_api_key",
			Pattern: regexp.MustCompile(`rubygems_[a-f0-9]{48}`),
			Redact:  "[REDACTED_RUBYGEMS_KEY]",
		},
	}
}
