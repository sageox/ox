Feature: Initializing a Repository for SageOx
  Devon runs `ox init` to bring a repository onto SageOx: it wires the repo to
  the team's endpoint, sets up the Ledger and Team Context, installs the agent
  hooks, and injects the prime marker so any AI coworker knows to prime at
  session start. Devon then commits the `.sageox/` directory so the whole team
  shares the same setup.

  See also: business-actions/init-repo.md
  See also: onboarding/status.feature
  See also: onboarding/doctor.feature
  See also: priming/prime-session.feature

  Rule: Init sets up SageOx for the repository

    Scenario: Devon initializes a fresh repository
      Given Devon is signed in and in an un-initialized git repository
      When he runs `ox init`
      Then ox initializes SageOx for the repository
      And ox creates the `.sageox/` configuration for the repo
      And ox tells him to run `ox agent prime` to load team context

    Scenario: Init injects the prime marker into the agent instructions
      Given Devon has just initialized the repository
      When he opens the repo's agent instructions
      Then they contain a marker telling an AI coworker to run `ox agent prime` at session start

  Rule: Re-initializing an already-initialized repo is safe

    Scenario: Devon runs init twice
      Given Devon has already initialized the repository
      When he runs `ox init` again
      Then ox tells him SageOx is already initialized
      And ox does not clobber his existing configuration

  Rule: The repo must be a git repository

    Scenario: Devon runs init outside a git repository
      Given Devon is in a directory that is not a git repository
      When he runs `ox init`
      Then ox tells him this is not a git repository
      And ox points him to initialize git first

  Rule: The team commits the .sageox directory to share the setup

    Scenario: Devon commits the SageOx configuration for the team
      Given Devon has initialized the repository
      When he commits the `.sageox/` directory and pushes
      Then Devon's teammates get the same SageOx setup when they pull
      And a teammate's AI coworker is told to prime from the shared marker
