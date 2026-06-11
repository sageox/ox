Feature: Listing and Inspecting Knowledge Bubbles
  Devon runs `ox kb list` to see every Knowledge Bubble he can access — his
  personal notes, profile, the team's shared bubbles, and the bubbles scoped to
  the current repo — in one unified list. He can filter by type and inspect a
  bubble to see what it holds and where it lives on disk. During the migration
  window, legacy Team Contexts and Ledgers appear in the same listing so the
  picture stays complete.

  See also: business-actions/inspect-bubbles.md
  See also: team-context/team-ctx.feature

  Rule: Listing shows every bubble the coworker can access

    Scenario: Devon lists all his Knowledge Bubbles
      Given Devon is signed in and a member of "Acme Engineering"
      When he lists his Knowledge Bubbles
      Then ox shows his personal, profile, team, and repo bubbles in one list

    Scenario: Legacy team contexts and ledgers appear during migration
      Given the team still has legacy team contexts and ledgers alongside new bubbles
      When Devon lists his Knowledge Bubbles
      Then ox shows the legacy team contexts and ledgers alongside the new bubbles
      And the list is complete

  Rule: Bubbles can be filtered by type

    Scenario Outline: Devon filters the bubble list by type
      Given Devon can access bubbles of several types
      When he lists bubbles filtered to "<type>"
      Then ox shows only the "<type>" bubbles

      Examples: Bubble types
        | type     |
        | personal |
        | profile  |
        | team     |
        | repo     |
        | custom   |

  Rule: A bubble can be inspected and located on disk

    Scenario: Devon inspects what a team bubble holds
      Given Devon sees a team bubble in his list
      When he inspects that bubble
      Then ox shows him what the bubble holds

    Scenario: Devon finds where a bubble lives on disk
      Given Devon wants the on-disk location of a bubble
      When he asks ox to resolve its path
      Then ox shows him the bubble's path on disk
