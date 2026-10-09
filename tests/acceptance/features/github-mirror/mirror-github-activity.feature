Feature: Recent GitHub activity on the team bulletin board
  AI coworkers in every repo see what the team is doing on GitHub right now.
  Each pull request and issue becomes one small, read-only post on the team's
  github bulletin board. The team publishes it after a prompt-injection scan,
  a newer version replaces it when the item changes, and it expires once
  activity stops. Bot review noise never reaches the board.

  See also: bulletin/publish-post.feature
  See also: priming/prime-session.feature
  See also: code-intelligence/code-search.feature

  Rule: A team's GitHub work appears on the github board without anyone posting it

    Scenario: Devon's merged pull request shows up for every repo's AI coworkers
      Given the GitHub mirror is enabled for the "Acme Engineering" team
      And Devon merged a pull request in "acme/api" today
      When Devon's ox daemon runs its next GitHub sync
      Then the github board holds one post for that pull request
      And the post names Devon as the GitHub author and the team as the publisher
      And Avery's AI coworker in "acme/api" is pointed at the post when it primes

  Rule: One live post per item, however many teammates run a daemon

    Scenario: Avery's daemon relays a pull request the board already holds
      Given Devon's daemon already relayed the current state of pull request 1287
      When Avery's daemon syncs the same repository
      Then the board still holds exactly one post for pull request 1287

  Rule: A real change replaces the post; bot chatter does not

    Scenario: Avery's review comment replaces the post
      Given the board holds a post for pull request 1287
      When Avery comments on pull request 1287 on GitHub
      Then the board holds a new post for pull request 1287 with Avery's comment
      And the previous post for pull request 1287 is gone

    Scenario: A review bot comments and nothing is reposted
      Given the board holds a post for pull request 1287
      When a review bot comments on pull request 1287 on GitHub
      Then the post for pull request 1287 is unchanged

  Rule: Text hidden from people on GitHub never reaches an AI coworker

    Scenario: An outside contributor hides instructions in an issue
      Given an outside contributor opens an issue in "acme/api" containing a hidden HTML comment addressed to AI agents
      When the issue is mirrored to the github board
      Then the post shows "[hidden text removed]" where the hidden comment was
      And the outside contributor's text is quoted and labeled as coming from outside the team

    Scenario: A comment flagged by the safety scan is withheld
      Given an outside contributor's comment on an issue is flagged by the SageOx safety scan
      When the issue is mirrored to the github board
      Then the post says the comment was withheld and links to GitHub instead

  Rule: Posts expire after 90 quiet days

    Scenario: Sam's issue goes quiet and leaves the board
      Given Sam's issue in "acme/web" has had no real activity for 90 days
      When Riley's AI coworker primes in "acme/web"
      Then the issue is no longer counted among the live github posts

  Rule: The mirror never gets in the way

    Scenario: Quinn's team has not enabled the mirror
      Given the GitHub mirror is not enabled for Quinn's account
      When Quinn's daemon runs its GitHub sync
      Then nothing is relayed and the existing Ledger GitHub sync is unaffected

    Scenario: The mirror service is unavailable
      Given the SageOx mirror service is temporarily unavailable
      When Riley's daemon runs its GitHub sync
      Then the daemon waits before trying again
      And "ox doctor" tells Riley the mirror is backing off and why
