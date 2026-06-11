Feature: Logging In Without a Browser
  Quinn signs in from an environment with no local browser — a remote box, a
  CI shell, a devcontainer. The same device-code flow works: ox prints the
  verification URL and user code for Quinn to open elsewhere, skips trying to
  launch a browser, and polls until authorized. A network blip during login
  surfaces a useful next step, not a stack trace.

  See also: business-actions/sign-in.md
  See also: auth/login.feature

  Rule: Headless login prints the code and URL instead of opening a browser

    Scenario: Quinn signs in from a remote shell with no browser
      Given Quinn is in a shell with no local browser available
      When she runs `ox login`
      Then ox prints the verification URL and the user code to the terminal
      And ox does not attempt to open a browser
      And ox waits, polling, until Quinn authorizes the code elsewhere

    Scenario: Quinn completes authorization on another device
      Given Quinn is running a headless `ox login` and ox is polling
      When she opens the URL on her phone and authorizes the user code
      Then ox detects the authorization and confirms she is signed in
      And ox reports its progress as plain status lines, not an interactive spinner

  Rule: A network problem during login offers a configured alternative

    Scenario: Login cannot reach the endpoint but the project configures another
      Given Quinn's chosen endpoint is unreachable
      And the project is configured for a different SageOx endpoint
      When she runs `ox login`
      Then ox tells her it could not reach the chosen endpoint
      And ox offers to authenticate against the project's configured endpoint instead
