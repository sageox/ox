Feature: Hearing Teammates' Murmurs as Whispers
  A murmur one coworker publishes is delivered as a whisper into other
  coworkers' active sessions, so the team stays coordinated without anyone
  having to poll. Riley hears Avery's murmur about contended files in time to
  route around the collision, and a critical murmur stands out from ambient
  chatter.

  See also: business-actions/murmur-wip.md
  See also: murmur/publish-wip.feature

  Rule: A published murmur reaches other coworkers as a whisper

    Scenario: Riley hears Avery's murmur in-session
      Given Avery published a murmur about the auth middleware
      And Riley is in an active session on the same repo
      When the murmur is relayed
      Then Riley hears it as a whisper in her session

  Rule: A whisper about contended files helps a teammate route around it

    Scenario: Riley avoids a collision after hearing a whisper
      Given Avery murmured that she is modifying the shared auth middleware
      When Riley hears the whisper while planning her own change
      Then Riley can route her plan around the contended files

  Rule: Importance is preserved through delivery

    Scenario: A critical murmur stands out from ambient chatter
      Given Avery published a critical murmur about a conflict
      When it is delivered to Riley
      Then the whisper conveys that it is critical, not ambient

  Rule: Expired murmurs are no longer delivered

    Scenario: A day-old murmur is not whispered to a new session
      Given Avery's murmur was published more than a day ago
      When Riley starts a fresh session
      Then ox does not whisper the expired murmur to her
