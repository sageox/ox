Feature: Searching Knowledge Bubbles
  Devon runs `ox kb query` to ask one question across the Knowledge Bubbles he
  names and get back ranked file hits, grouped per bubble. Search finds the
  file; Devon then reads it from the bubble's local mount. The answer is
  honest per bubble: a bubble that matched nothing, one that was never
  indexed, and one that failed are reported distinctly — and a bubble Devon
  cannot search never reveals why.

  See also: list-bubbles.feature

  @wip
  Rule: One question searches the named bubbles and returns ranked file hits per bubble

    Scenario: Devon searches two bubbles with one question
      Given Devon is signed in and can read the bubbles "#engineering" and "#platform"
      When he queries them with "how do we batch relay spans"
      Then ox shows ranked file hits grouped under each bubble, in the order he named them
      And each hit shows the file's path, title, and a short snippet of the matching text

    Scenario: Devon must name at least one bubble and a question
      Given Devon is signed in
      When he runs the query command with only one argument
      Then ox explains that the last argument is the question and every argument before it names a bubble

    Scenario: Hits point Devon at files, not answers
      Given Devon's query matched a curated file
      When he reads the result
      Then ox tells him the hit is a file to read from the bubble's local mount

  @wip
  Rule: Every bubble's outcome is reported honestly — empty, never-indexed, and failed are distinct

    Scenario: A bubble that matched nothing is not confused with one that was never indexed
      Given "#engineering" is indexed but has no file matching Devon's question
      And "#platform" has never been indexed for search
      When Devon queries both bubbles
      Then ox reports "#engineering" as having no matches
      And reports "#platform" as not indexed yet
      And the two reports read differently

    Scenario: A bubble Devon cannot search is reported without revealing why
      Given Devon queries a bubble that does not exist alongside one he can read
      When the results come back
      Then ox reports the unreadable bubble as not searchable for him
      And does not reveal whether it exists, is off-limits, or is a type that is not indexed

  @wip
  Rule: When search is not enabled, bubbles remain readable and ox says so

    Scenario: Devon's team does not have KB file search enabled
      Given Devon's server does not offer KB file search
      When Devon queries a bubble
      Then ox explains KB file search isn't enabled for his team yet
      And points him at listing bubbles and reading from a bubble's local mount
