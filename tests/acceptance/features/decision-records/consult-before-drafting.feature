Feature: Consulting Decision Records Before Drafting
  Before Avery drafts or edits a Decision Record — or picks up an issue that a
  past decision already settled — ox surfaces the records relevant to her work,
  so a decision does not get made twice and a new record does not quietly
  contradict one the team already accepted. ox walks the repo's Decision Records
  fresh and scores them locally, at zero LLM and zero network cost. The promise
  that matters: when Avery describes her work the way she actually would — a
  paragraph, an issue body, a whole plan — the record that governs it is found,
  not only when she already types its title.

  See also: plan-enrichment/enrich-while-drafting.feature
  See also: code-intelligence/insights.feature

  Rule: A relevant record is found even for long, real-world input

    Scenario: Avery describes her work in a full paragraph and the governing record is found
      Given the team has a Decision Record that governs feature flags
      When Avery consults ox with a paragraph describing a feature-flag rollout
      Then ox surfaces that record as a related decision
      And it is surfaced whether her description is two words or two hundred

    Scenario: A longer description never finds less than a shorter one
      Given a Decision Record that a short query surfaces
      When Avery adds more words describing the same work
      Then ox still surfaces that record
      And its relevance does not fall away as her description grows

  Rule: Consulting a topic returns what drafting a plan on it returns

    Scenario: Both entry points agree over the same records
      Given a repo with a Decision Record about feature flags
      When Avery consults that topic before drafting a record
      And Sam enriches a plan on the same topic
      Then both are shown the same governing record

  Rule: An unreadable corpus is reported, never treated as a verified absence

    Scenario: A decision folder whose files are not records is flagged, not silently empty
      Given a decision folder that holds markdown that does not parse as records
      When Avery consults a topic
      Then ox tells her the corpus could not be read and why
      And ox does not tell her that "no prior decision exists" as a verified fact

    Scenario: A genuinely empty corpus still lets Avery state absence as verified
      Given a repo with no Decision Records at all and every source readable
      When Avery consults a topic
      Then ox reports that nothing matched
      And Avery may state "no prior team decision found" as a verifiable claim

  Rule: Near-misses can be surfaced on request

    Scenario: Avery asks to see what the relevance floor discarded
      Given records that match a topic only weakly
      When Avery consults the topic and asks ox to explain
      Then ox lists the candidates it dropped below the relevance floor with their scores
      And she can tell "nothing was relevant" apart from "records were found and dropped"
