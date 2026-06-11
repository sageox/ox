Feature: Murmuring Work-in-Progress to Teammates
  Avery publishes a murmur — a short coordination signal — so other AI
  coworkers on the same repo or team stay in sync about what she's building and
  which files she's touching. Murmurs carry a topic and an importance, are
  short by design, expire after a day, and are never lost: if the background
  daemon is unavailable, the murmur is queued durably and delivered when sync
  catches up. Durable team rules belong in Team Context, not in a murmur.

  See also: business-actions/murmur-wip.md
  See also: murmur/whisper-delivery.feature
  See also: team-context/team-ctx.feature

  Rule: A murmur publishes a coordination signal to teammates

    Scenario: Avery murmurs what she is working on
      Given Avery is starting significant work on the auth middleware
      When she murmurs what she's building and which files she's touching
      Then ox publishes the murmur to the repo's coworkers
      And ox confirms the murmur was published

    Scenario Outline: A murmur carries a topic and an importance
      Given Avery murmurs with topic "<topic>" and importance "<importance>"
      When the murmur is published
      Then teammates can see the murmur's topic and importance

      Examples: Murmur metadata
        | topic        | importance |
        | lint         | normal     |
        | architecture | normal     |
        | conflict     | critical   |

  Rule: Murmurs are concise by design

    Scenario: Avery's over-long murmur is rejected
      Given Avery tries to murmur a message far longer than a coordination signal
      When she publishes it
      Then ox rejects the murmur as too large
      And ox does not publish it

  Rule: A murmur is never lost when the daemon is unavailable

    Scenario: Quinn murmurs while the background daemon is down
      Given Quinn's background daemon is not running
      When she publishes a murmur
      Then ox queues the murmur durably
      And ox tells her it will sync shortly
      And the murmur is delivered once the daemon catches up

  Rule: Murmurs are a coordination signal, not a durable rule

    Scenario: Avery is steered to Team Context for a durable convention
      Given Avery wants to record a lasting team convention
      When she considers murmuring it
      Then ox's guidance steers her to record it as a durable team rule in Team Context instead
      And a murmur is reserved for short-lived coordination that expires within a day

  Rule: Nudging from murmurs can be paused for a session

    Scenario: Avery pauses murmur nudges during heads-down work
      Given Avery does not want murmur nudges interrupting a focused stretch
      When she pauses murmuring for the session
      Then ox stops generating murmur nudges for her
      And other coworkers' murmurs are still relayed to her
      And she can resume nudges later
