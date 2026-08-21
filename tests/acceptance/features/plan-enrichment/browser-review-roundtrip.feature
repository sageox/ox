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
