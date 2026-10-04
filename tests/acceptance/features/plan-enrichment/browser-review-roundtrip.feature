Feature: The Browser Review Round-Trip
  Devon reviews a plan in his browser and Avery, the authoring AI coworker, acts
  on what he leaves there. Devon marks up a section, adds a note, and submits —
  and those exact marks reach Avery without Devon copying anything into a chat.
  Avery addresses each item and Devon's open page updates to show it resolved,
  so the whole back-and-forth happens on a live page. The loop only accepts
  feedback that carries the valid review token the served page was handed, keeps
  every reviewer's marks attributed to them, and holds a reviewer's in-progress
  marks safe across a dropped connection.

  See also: business-actions/review-plan.md
  See also: plan-enrichment/review-loop.feature
  See also: plan-enrichment/render-and-present.feature

  Rule: A reviewer's browser feedback reaches the authoring coworker

    Scenario: Devon marks up a section in the browser and Avery receives it
      Given Devon is reviewing one of Avery's plans on the served review page
      When Devon marks a section, writes a note, and submits his feedback
      Then Avery receives that item with the section it was left on and the note
      And Devon did not have to copy his feedback anywhere for Avery to see it

    Scenario Outline: Devon leaves a <mark> on the plan and it reaches Avery intact
      Given Devon is reviewing one of Avery's plans on the served review page
      When Devon leaves a <mark> on a section with a note and submits
      Then Avery receives that item carrying its section and note

      Examples: The marks a reviewer can leave
        | mark              |
        | request-for-change |
        | flag              |
        | comment           |

    Scenario: Devon submits several marks at once and they arrive as one round
      Given Devon marked up three different sections of Avery's plan
      When Devon submits his feedback
      Then Avery receives all three items in one round
      And each item is still tied to the section it was left on

  Rule: The authoring coworker acts on browser feedback and the reviewer sees it resolve live

    Scenario: Avery addresses an item and Devon's open page shows it resolved
      Given Devon left an open item on Avery's plan and is still on the page
      When Avery addresses the item and marks it resolved
      Then Devon's page shows that item as addressed without Devon reopening it

    Scenario: Devon accepts an addressed item and it stops being open
      Given Avery addressed an item that Devon had raised
      When Devon accepts the fix on the page
      Then the item is no longer counted as open for the plan

    Scenario: Devon reopens an item he is not satisfied with
      Given Avery marked an item addressed but Devon is not satisfied
      When Devon reopens the item on the page
      Then the item counts as open again
      And it returns to Avery as work still to do

    Scenario: Devon approves the plan from the browser and the review closes out
      Given Devon is satisfied with Avery's plan in review
      When Devon approves it from the page
      Then ox records the plan as approved
      And Avery's review loop ends

  Rule: The browser review loop is trustworthy and attributable

    Scenario: Feedback without the served page's review token is refused
      Given someone tries to submit feedback without the review token Devon's page was handed
      When the submission is made
      Then ox refuses it
      And no feedback reaches Avery from that submission

    Scenario: Devon and Riley review the same plan and each mark is attributed
      Given Devon and Riley both leave feedback on the same plan
      When Avery looks at the feedback
      Then each mark is attributed to the reviewer who left it
      And when Devon and Riley disagree on the same spot, ox surfaces the disagreement rather than dropping one

    Scenario: Quinn's connection drops mid-review and his marks survive
      Given Quinn has marked up a plan in the browser but not yet submitted
      When his connection to the review loop drops and then comes back
      Then Quinn's in-progress marks are still on the page
      And he can submit them once he is back

  Rule: A reviewer can comment on exactly the words they highlight

    Scenario: Devon highlights a phrase and Avery receives those exact words
      Given Devon is reviewing one of Avery's plans on the served review page
      When Devon highlights a phrase in a section, writes a note, and submits
      Then Avery receives the item with the highlighted words, the section, and the note
      And Devon's page shows the phrase highlighted

    Scenario: Devon still marks a whole section with a plain click
      Given Devon highlighted a phrase in a section
      When Devon clicks elsewhere in that section and leaves a note
      Then that note is about the whole section, not the phrase
      And Avery receives the highlight and the section note as separate items

    Scenario: Devon highlights a word the section repeats
      Given a word appears twice in one section of Avery's plan
      When Devon highlights just that word
      Then ox asks Devon to highlight a longer phrase
      And no comment is left on an ambiguous spot

    Scenario: Avery addresses a highlight and Devon's page shows it resolved
      Given Devon left a highlight on Avery's plan and is still on the page
      When Avery addresses the highlight and marks it resolved
      Then Devon's page shows the highlighted words as addressed without Devon reopening it

    Scenario: Avery rewrites the highlighted words and Devon can still accept the fix
      Given Avery addressed Devon's highlight by rewriting the words it covered
      When Devon looks at the comments on the page
      Then the highlight is still listed, marked addressed with its text changed
      And Devon can accept or reopen it from there

    Scenario: Devon reopens a highlight and Avery gets the words back
      Given Avery marked Devon's highlight addressed but Devon is not satisfied
      When Devon reopens it on the page
      Then the item that returns to Avery still quotes the highlighted words

    Scenario: Quinn's unsent highlight survives the review server restarting
      Given Quinn highlighted a phrase and saved a note but has not submitted it
      When the review server stops and is started again
      Then Quinn's page reconnects on its own with the phrase still highlighted and the note intact
      And Quinn can submit it once the page is back

    Scenario: Quinn reloads the page while the review server is down
      Given Quinn opened the review page and left an unsent highlight
      When the review server stops and Quinn reloads the page
      Then the plan still shows, marked offline, with the phrase still highlighted
