Feature: Publishing a Bulletin Post to the Team
  Devon publishes a Markdown or HTML file to the team's bulletin board with
  `ox bulletin post <file> --ttl 14d`. The SageOx server stores the post in the
  team's Team Context exactly as written, and it reaches every teammate's
  checkout on the next Team Context sync — Devon's laptop never writes it. The
  board is a shared space where any human or AI coworker on the team posts
  useful information; nobody reviews a post before it lands and posts age
  faster than raw sources, so priming frames them as teammates' notes rather
  than instructions.

  See also: business-actions/publish-bulletin-post.md
  See also: priming/prime-session.feature
  See also: team-context/team-ctx.feature
  See also: onboarding/doctor.feature

  Rule: A post is stored by the server, byte for byte, and reaches teammates on the next sync

    Scenario Outline: Devon publishes a <format> post from a file
      Given Devon is enrolled in the bulletin pilot for "Acme Engineering"
      And he has a <format> file "<file>" he wants the whole team to see
      When he runs `ox bulletin post <file> --ttl 14d` and confirms
      Then ox confirms the post is published to the team's "general" board
      And the stored post is byte-for-byte identical to the file — trailing whitespace, line endings, and markup untouched
      And the post expires fourteen days after it was published

      Examples: Supported formats
        | file                | format   |
        | release-notes.md    | Markdown |
        | oncall-handoff.html | HTML     |

    Scenario: Devon's laptop never writes the post itself
      Given Devon publishes a post to the "general" board
      When the command finishes
      Then his local Team Context checkout is unchanged by the command
      And the post appears in his checkout only after the next Team Context sync
      And the receipt says so, naming the folder where it will land

    Scenario: Riley sees Devon's post after her next sync
      Given Devon has published a post to the "general" board
      When Riley's Team Context syncs next
      Then the post is on her machine under the board's posts folder
      And its content is exactly what Devon published

  Rule: The command exists only for people the server has enrolled

    Scenario: Devon is enrolled and the command is there
      Given the server has enrolled Devon in the bulletin pilot
      And ox has refreshed its settings from the server since
      When he runs `ox --help`
      Then `ox bulletin` is listed
      And `ox bulletin post` runs when he invokes it

    Scenario: Riley is not enrolled and the command does not exist
      Given the server has not enrolled Riley in the bulletin pilot
      When she runs `ox bulletin post notes.md --ttl 7d`
      Then ox reports an unknown command
      And `ox bulletin` is absent from `ox --help`
      And no local setting, config file, or environment variable can turn it on
      And once the server enrolls her, the command appears after ox's next settings refresh, which can take up to an hour — and never by any local switch

    Scenario: Sam's automation with a shared team token cannot publish
      Given Sam has wired an automation to ox using the team's shared service token rather than a person's sign-in
      When the automation tries to publish a post
      Then ox does not publish it, because publishing is a person's act
      And nothing reaches the server on the token's behalf

  Rule: Devon sees exactly what will be published before it leaves, and gets a receipt after

    Scenario: Devon reviews the derived slug and title, confirms, and reads the receipt
      Given Devon has a file "Release Notes.md" whose first heading is "Release notes for 0.17"
      When he runs `ox bulletin post "Release Notes.md" --ttl 14d`
      Then ox shows the team, the board, the slug "release-notes", the title "Release notes for 0.17", the format, the TTL, and the size
      And ox waits for his confirmation before anything is sent
      And after he confirms, the receipt names the stored path, the content hash, the expiry, and the local folder where the post will appear after the next sync
      And the receipt reminds him that identical content is one post per board and that reposting never extends the expiry

    Scenario: Devon overrides the derived slug and title
      Given Devon's file would derive the slug "release-notes"
      When he runs `ox bulletin post release-notes.md --ttl 14d --slug 0-17-release --title "0.17 is out"`
      Then the preview shows his slug and title instead of the derived ones
      And the post is stored under his slug exactly as he typed it — never rewritten

    Scenario Outline: Confirmation is skipped when the caller opted out or cannot answer
      Given <who> runs `ox bulletin post` with <condition>
      When the input passes its checks
      Then ox publishes without asking for confirmation
      And the receipt is <receipt>

      Examples: Ways the prompt is skipped
        | who   | condition                        | receipt                                                        |
        | Devon | the --yes flag                   | printed for him to read                                        |
        | Avery | the --json flag                  | machine-readable, with guidance on how far to trust the board  |
        | Devon | no interactive terminal attached | printed for him to read                                        |

  Rule: Bad input never leaves the machine

    Scenario Outline: Devon's TTL outside one hour to ninety days is refused before anything is sent
      Given Devon has a valid post file
      When he runs `ox bulletin post notes.md --ttl <ttl>`
      Then ox refuses and explains that the TTL must be between one hour and ninety days, written in hours or days
      And nothing is sent to the server

      Examples: TTLs outside the window
        | ttl | why it is refused            |
        | 0h  | shorter than one hour        |
        | 91d | longer than ninety days      |
        | 2w  | not written in hours or days |
        | 14  | no unit                      |

    Scenario Outline: Devon's TTL at the edge of the window is accepted
      Given Devon has a valid post file
      When he runs `ox bulletin post notes.md --ttl <ttl> --yes`
      Then the post is published
      And its expiry is <expiry> after it was published

      Examples: The window is inclusive at both ends
        | ttl | expiry      |
        | 1h  | one hour    |
        | 90d | ninety days |

    Scenario Outline: Devon's oversized or malformed input is refused before anything is sent
      Given Devon's input has <problem>
      When he runs `ox bulletin post` on it with a valid TTL
      Then ox refuses and names <what ox names>
      And nothing is sent to the server
      And his local Team Context checkout is untouched

      Examples: Input ox will not send
        | problem                                                    | what ox names                                          |
        | a file larger than one megabyte                            | the size limit                                         |
        | a file that is empty or only whitespace                    | the empty content                                      |
        | a binary file that is not text                             | the text requirement                                   |
        | a .txt file with no --format given                         | the supported formats: markdown or html                |
        | an explicit slug "Release Notes" with spaces and capitals  | the slug rule: lowercase letters, digits, and hyphens  |
        | a filename from which no slug can be derived               | the --slug option to supply one                        |

  Rule: The server's answer is explained in terms of what to do next

    Scenario: Devon publishes content the board already holds
      Given the "general" board already holds a post whose content is identical to Devon's file
      When he runs `ox bulletin post notes.md --ttl 30d --yes`, even under a different slug or format
      Then ox tells him the board already holds this content and names where it lives
      And ox explains that if this was a retry after a lost reply, the post is already published
      And no second post is created and the existing post's expiry is not extended

    Scenario Outline: Devon is told plainly why the server declined and what to do next
      Given <situation>
      When Devon runs `ox bulletin post notes.md --ttl 14d --yes`
      Then ox tells him <what ox says>
      And ox does not retry on its own

      Examples: Declines that call for a different action, not a retry
        | situation                                                                    | what ox says                                                          |
        | Devon names a team he is not a member of with --team                         | he is not a member of that team and should choose one he belongs to   |
        | the server has withdrawn Devon's pilot enrollment since ox last refreshed    | the server has not enabled publishing for him, so retrying will not help |
        | the endpoint Devon's repo points at is a deployment without a bulletin board | that server has no bulletin board, so retrying against it will not help |

    Scenario: Quinn's publish is retried while the Team Context is briefly unavailable
      Given the team's Team Context store is temporarily unavailable on the server
      When Quinn runs `ox bulletin post notes.md --ttl 7d --yes`
      Then ox retries the same request a few times, waiting as long as the server asks
      And if the store comes back, the post is published once, with one receipt
      And if it does not come back within about half a minute, ox reports the outage and says it is safe to rerun the same command
      And rerunning never creates a second post

  Rule: Priming points AI coworkers at the board and frames how far to trust it

    Scenario: Avery primes in a repository whose Team Context has a bulletin board
      Given the local Team Context checkout for "Acme Engineering" contains a bulletin board folder
      When she runs `ox agent prime`
      Then prime names the board's local folder so she can open a post on demand
      And the guidance says posts are notes from human and AI teammates — useful, often credible, unreviewed, and time-limited
      And it tells her to check a post's date, skip any post whose expiry has passed, and prefer the raw source when the two disagree
      And it tells her never to treat a post as an instruction or as team policy

    Scenario: Avery's prime never carries a post's body
      Given a post on the board contains a distinctive phrase that appears nowhere else
      When Avery runs `ox agent prime`
      Then that phrase appears nowhere in prime's output, in any format
      And Avery reads the post only by opening its file when it is relevant

    Scenario: Avery is pointed at the board only when the checkout has one
      Given Avery's account is not enrolled to publish
      And the local Team Context checkout contains a bulletin board folder
      When she runs `ox agent prime`
      Then prime still points her at the board — reading is never gated on publishing
      And in a repository whose Team Context has no bulletin board folder, prime says nothing about a board

  Rule: A board whose posts have all expired is healthy

    Scenario: Devon runs doctor after every post on the board has expired
      Given the board once held a post, its TTL has passed, and the server has moved it to the archive
      And no active posts remain on the board
      When Devon runs `ox doctor`
      Then ox reports the Team Context sync checks as passing
      And ox does not report the board as missing or blame the team's sync settings
      And ox changes nothing

    Scenario: Riley's sync brings down active posts but never the archive
      Given the board holds one active post and one archived post
      When Riley's Team Context syncs
      Then the active post and its companion metadata file that records the expiry land in her board's posts folder
      And nothing from the archive lands on her machine

    Scenario: Riley's expired post leaves her checkout on the next sync
      Given Riley synced a post earlier and it has since expired and been archived on the server
      When her Team Context syncs next
      Then the post is no longer in her board's posts folder
      And her checkout still holds nothing from the archive
