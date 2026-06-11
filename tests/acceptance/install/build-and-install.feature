Feature: Building and Installing ox
  Devon builds ox from source and installs it onto their PATH so the `ox`
  command is available in every shell and every repo. After installing, `ox
  version` confirms the build, and the first run in a repo points Devon at the
  next step rather than failing silently.

  See also: business-actions/install-ox.md
  See also: onboarding/init-repo.feature
  See also: upgrade/upgrade-cli.feature

  Rule: Building from source produces an ox binary

    Scenario: Devon builds ox from source
      Given Devon has cloned the ox repository
      When he builds ox from source
      Then an ox binary is produced
      And the bundled adapter binaries are produced alongside it

  Rule: Installing puts ox on the PATH

    Scenario: Devon installs ox and finds it on his PATH
      Given Devon has built ox from source
      When he installs ox
      Then the `ox` command is available in a new shell
      And running `ox` with no arguments prints the top-level help

  Rule: ox version reports the installed build

    Scenario: Devon confirms the installed version
      Given Devon has installed ox
      When he runs `ox version`
      Then ox prints its version, build date, and commit
      And the printed version matches the build he just installed

  Rule: First run in a repo guides rather than fails

    Scenario: Devon runs ox in a repo that is not yet initialized
      Given Devon has installed ox
      And he is in a git repository that has not been initialized for SageOx
      When he runs `ox status`
      Then ox tells him the repository is not initialized
      And ox points him to run `ox init` to set it up

    Scenario: Devon runs an ox command before logging in
      Given Devon has installed ox but has not logged in
      When he runs a command that needs the SageOx cloud
      Then ox tells him he is not authenticated
      And ox points him to run `ox login`
