Feature: Reading a Visual Walkthrough as Joined Moments
  When Devon records a Visual walkthrough and shares it, Avery does not get a
  bare list of clicks to puzzle over. `ox walkthrough` joins, for each moment,
  what Devon said, the thing on screen he was talking about, and what the screen
  did next, and it offers a zoomed picture of that spot with the thing outlined.
  Long pauses and typing collapse to one line each. Pictures arrive only when
  Avery asks for them, and the short-lived links behind them are never shown.
  A recording that has none of this still reads exactly as it did before.

  See also: session-recording/list-and-view.feature
  See also: team-context/query.feature

  Rule: Each moment joins the words, the element, and what happened next

    Avery's job is to turn "make this bigger" into the right change. That needs
    the sentence and the button it was about in the same place, not two lists
    to line up by timestamp.

    Scenario: Avery reads a click together with what Devon said
      Given Devon recorded a Visual walkthrough and clicked "Start free trial" while saying "make this bigger"
      When Avery reads the walkthrough
      Then Avery sees a click on the button "Start free trial"
      And Avery sees the sentence Devon said at that moment
      And Avery sees that a sheet titled "Start your trial" opened right after

    Scenario: Avery reads selected text as a moment of its own
      Given Devon selected the text "Annual billing saves 20%" while talking about it
      When Avery reads the walkthrough
      Then Avery sees a selection moment showing the selected text
      And Avery sees the sentence Devon said while selecting it

    Scenario: Avery reads a plural reference and an unnamed target honestly
      Given Devon said "these two plans" while pointing at several buttons
      And Devon rested the pointer on a table that has no name of its own
      When Avery reads the walkthrough
      Then Avery sees the names of the buttons Devon meant, with how many more there were
      And Avery sees the table's enclosing section named as the place, not as the thing clicked

  Rule: Chapters let Avery read one part at a time

    A ten-minute walkthrough is several conversations about several pages.
    Avery should be able to see that shape and read just the part a request is
    about.

    Scenario: Avery sees the walkthrough split into chapters
      Given Devon moved from the pricing page to checkout to a plan comparison
      When Avery reads the walkthrough
      Then each chapter appears as a headed section with its start time and page address
      And each moment sits under the chapter it happened in

    Scenario: Avery reads one chapter
      Given Devon's walkthrough has a chapter titled "Checkout"
      When Avery reads only the chapter "Checkout"
      Then Avery gets that chapter's moments, pauses and shortcuts
      And Avery gets nothing from the chapters before or after it

    Scenario: Avery reads a long chapter's words page by page
      Given Devon's chapter "Plan comparison" has more words than fit on one page
      When Avery reads the chapter's words and follows the continuation ox offers
      Then every following page continues inside the same chapter
      And no line of speech is served twice or skipped

    Scenario: Avery limits the read and is told chapters were left out
      Given Devon's walkthrough has three chapters
      When Avery limits the read to two entries
      Then Avery sees two chapters
      And ox says one more chapter exists and how to read it

    Scenario: Avery names a chapter that is not there
      Given Devon's walkthrough has chapters titled "Pricing", "Checkout" and "Plan comparison"
      When Avery reads only the chapter "Settings"
      Then ox refuses and lists the chapters with their start times
      And ox does not hand back the whole walkthrough in its place

    Scenario: Avery asks for a chapter of a recording that has none
      Given Devon's recording has no chapters on the server
      When Avery reads only the chapter "Pricing"
      Then ox refuses and says why chapters are not available

  Rule: Quiet stretches and typing collapse to one line

    A reader who has to scan thirty seconds of "pointer still" rows is paying
    for nothing. The pause is one fact.

    Scenario: Avery sees a long pause as a single line
      Given Devon stopped talking and moved nothing for 38 seconds
      When Avery reads the walkthrough
      Then Avery sees one line saying 38 seconds passed with nothing said and the pointer still

    Scenario: Avery sees typing as a single line naming the field
      Given Devon typed into a field named "Search plans" for a while
      When Avery reads the walkthrough
      Then Avery sees one line saying Devon was typing in that field

  Rule: Shortcuts, screen changes and extra tracks are said plainly

    Scenario: Avery sees a keyboard shortcut and how often Devon pressed it
      Given Devon chose to show keyboard shortcuts and pressed Command-K twice in a row
      When Avery reads the walkthrough
      Then Avery sees one line for the shortcut at that moment with a count of two

    Scenario: Avery sees where the screen changed in words
      Given something changed in the upper right of the screen after Devon's click
      And nothing else explains the change
      When Avery reads the walkthrough
      Then Avery sees that the screen changed, placed as "top-right"
      And Avery is not given coordinates to interpret

    Scenario: Avery learns Devon had a camera and cleaned audio without being handed them
      Given Devon recorded with a camera on and the narration was cleaned up
      When Avery reads the walkthrough
      Then ox says a camera track and a cleaned narration track exist
      And ox offers neither the camera pictures nor any link to them

  Rule: Pictures come only when Avery asks, and links never show

    The picture links the server hands out are credentials that expire. Printing
    one puts a working key in a transcript, a log, or a bug report.

    Scenario: Avery asks for the pictures and gets files she can open
      Given Devon's walkthrough has zoomed pictures for its moments
      When Avery reads it asking for the pictures
      Then each moment's picture is saved on Avery's machine and she is given the file to open
      And the files are real pictures
      And ox does not send Devon's sign-in to wherever the pictures are kept

    Scenario: Avery reads again and nothing is downloaded twice
      Given Avery already fetched the pictures for this walkthrough
      When Avery reads it asking for the pictures again
      Then ox uses the saved files and downloads nothing

    Scenario: Avery asks for the whole-frame picture as well
      Given Devon's walkthrough has a whole-frame picture for each moment
      When Avery reads it asking for the whole-frame pictures too
      Then each moment also gets its whole-frame picture saved and listed

    Scenario: Avery asks for a very long stretch
      Given Devon's walkthrough has more moments than ox will fetch in one request
      When Avery reads all of it asking for the pictures
      Then ox fetches a bounded number of pictures
      And ox tells Avery to narrow the window for the rest

    Scenario: A picture that was cut short is fetched again
      Given a saved picture on Avery's machine that was cut off part way
      When Avery reads the walkthrough asking for the pictures
      Then ox does not treat the damaged file as a picture
      And ox fetches it again and replaces it with the whole picture

    Scenario: One picture cannot be fetched
      Given the place the pictures are kept refuses one of them
      When Avery reads the walkthrough asking for the pictures
      Then Avery still gets every moment and every other picture
      And ox warns which moment's picture is missing

    Scenario: Avery never sees a picture link
      Given Devon's walkthrough has zoomed pictures for its moments
      When Avery reads it, as data or as text, with or without asking for the pictures
      Then no short-lived picture link appears anywhere in what ox prints

  Rule: A walkthrough without this data reads exactly as before

    Older recordings, recordings still being processed, and plain video uploads
    have no joined moments. They are not broken, and Avery should not be told
    they are.

    Scenario: Avery reads a recording that has no joined moments yet
      Given Devon's recording has no joined-moment data on the server
      When Avery reads the walkthrough
      Then Avery gets the same screen evidence ox gave before this existed
      And ox adds one short note that joined moments are not available

    Scenario: The server cannot be reached
      Given the server is having an outage
      When Avery reads the walkthrough
      Then Avery gets the same screen evidence ox gave before this existed
      And ox adds one short note that joined moments could not be fetched
