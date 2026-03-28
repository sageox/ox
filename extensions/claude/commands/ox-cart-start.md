Claim a cart and start working on it.

## Post-Command (REQUIRED)

After the command completes:
1. Parse the JSON output to get the cart title
2. **Rename this Claude session** using `/rename` with a short kebab-case name derived from the cart title (e.g. cart title "Auth middleware rate limiting" → `/rename auth-rate-limiting`)
3. Display a brief confirmation: the cart ID, title, and that it's now in_progress assigned to you

$ox carts start $ARGUMENTS --json
