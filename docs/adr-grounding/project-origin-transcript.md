---
audience: ai
ai_editing: allowed
refreshable: true
---

# Sage Ox Idea Conception Meeting

**Date:** December 9, 2025
**Participants:** Ajit Banerjee, Ryan Snodgrass, and others

---

## Enterprise Developer Experience Gap

**Ajit Banerjee:**
Why did Amazon AGI want to use Hugging Face inside? It's like, what are developers already using? Hugging Face.

They have come from their colleges and from other companies, and they know Hugging Face. But then they come in here, and then we tell them to use this bullshit. So much productivity is lost. But that's Claude now. Claude, before it comes into your company, does not know anything about your company's weird structures - whatever Brazil, Apollo, blah blah is. So what Claude is used to doing is what the kids in Prime foundations are going to be doing.

If we can build, as part of Sage Ox, something for the solution state - they don't need to go all in for this. When they start off, they don't need to use this for everything. We can just deploy `sageox.dev.walmart.com`, and then we can be like: "Hey, for what you used to use Heroku - point it to `heroku.sageox.dev`. If you're using Fireworks, point it to this thing. Use their CLIs with some tweaks."

**Ryan Snodgrass:**
When you say "use our CLIs," what do you mean?

**Ajit Banerjee:**
So there's a Heroku command line, Railway command line. That command line maybe is supported by our tool, because we can generate any code that we want now on the fly - Claude is generating that code - except that instead of it going into Heroku, it's going into Walmart's embrace of the cool kids outside.

This is all based on the agent experience idea that came out of Steve Yegge's post. The agent experience is from outside, so the enterprise is a black box for these agents. Whatever shit that you have built in your internal tool base is a black box. So if you can expose a set of these things which help developer productivity, it might help.

---

## Bridging Enterprise and External Tools

**Ryan Snodgrass:**
I agree with you at a high level. I'm having a hard time wrapping my head around what that really means. Obviously, providing the enterprise with the cool kids' tools is good, but how do you embrace what they've already got in the enterprise? How do you bridge that?

**Ajit Banerjee:**
These tools seem to have a CLI, they have a UX. Developers love those CLI and UX. These things take a lot of work to translate inside. That's the reason why Hugging Face doesn't want to go into Amazon.

The learning is: there are people inside Amazon who want to use Hugging Face, Heroku, and Together.ai - and they cannot because of friction.

**Ryan Snodgrass:**
They want to because it's what they know, because it's sexier.

**Ajit Banerjee:**
Because it's outside, and because Claude knows about it too. Claude doesn't know about Brazil and Apollo.

**Ryan Snodgrass:**
Claude can know about Brazil and Apollo very easily. What do you need to know about Brazil Apollo? You need to know why we structure the repositories this way, the tooling that you use. You could write it up in a 2000-word summary and Claude will load that every time it starts up and already know that context.

---

## Agent Experience and Common Best Practices

**Ajit Banerjee:**
Work with me here. Having agent-like workflows makes the common best practices the same across companies. I'm playing with that statement of agent experience.

Two years ago, if you were in Google and I was in Facebook and we both wanted to build something, our two solutions would look very different. But now, because both of us are using Claude, you're going to get guided into more similar pathways, because Claude is being trained on similar ideas.

Can we take advantage of this on the infrastructure side? Making the common pathways that Claude is going to pick available within a company, even though the companies are different? This is entirely Steve Yegge's insight.

---

## Platform-as-a-Service Vision

**Ajit Banerjee:**
In the past, I've been playing with this thing in the multi-cloud aspect because that has been, in my mind, a cost-saving aspect. I've been looking at negotiating power, looking at Hugging Face, looking at hypocritical AI. These guys have got it right.

But what if we could build some app layers on Sage Ox which are not just infrastructure as code, but actually like platform-as-a-service which Heroku has - within the company, on demand - so that you don't actually ever have to do infrastructure as code at all as a developer?

**Ryan Snodgrass:**
Platform as a service - you're just doing the vertical deployment?

**Ajit Banerjee:**
But kind of embody that. So, okay, I'm pushing with your thing of: what happens if infrastructure is just "I don't give a shit"?

**Ryan Snodgrass:**
Infrastructure is just "I don't give a shit."

**Ajit Banerjee:**
Just assume that whole thing goes away. You're assuming that people want to hear what infrastructure is called. Fuck that shit. Just make it go away. We know that people have made it go away with these solutions - when you do Heroku, you never think about infrastructure.

**Ryan Snodgrass:**
They've made one vertical case and made it so easy you don't have to think about IaC.

---

## Multi-Cloud and Industry Verticals

**Ajit Banerjee:**
The point we made about the chaos is that nowadays there are different infrastructures popping up. You can look at what is getting hard - basically any company that hits like some billion dollars in revenue in this space, we can replicate it for our work.

**Ryan Snodgrass:**
Can you tell me more about this? What exactly does this provide?

**Ajit Banerjee:**
Fine-tuning as a service. That's what Onchi was talking about. He went for his interview. Until then, just use these services. They are really good because you never have to pay for hosting, and then you just pay for inference costs.

**Ryan Snodgrass:**
I saw a deployment. It's just fine-tuning?

**Ajit Banerjee:**
And then you get that model as an endpoint.

**Ryan Snodgrass:**
Oh, you get a hosted model.

**Ajit Banerjee:**
And so there's Hugging Face too - the set of tools which the world needs includes GitLab, Hugging Face.

**Ryan Snodgrass:**
What are people deploying? I'm thinking - back to this vertical. This is like web stack, frontend, blah blah. This is tuning. There's agents somehow. This is kind of Bedrock core.

What else are people deploying in enterprises?

**Ajit Banerjee:**
My point is, let's say you have five or six of these.

**Ryan Snodgrass:**
We're trying to figure out - there must be more of these.

**Ajit Banerjee:**
And so this is all the footprint of what we need to replicate.

---

## Ad Hoc Infrastructure vs. Set Patterns

**Ryan Snodgrass:**
My point is: can Sage Ox be everything? You've always talked about this ad hoc, but to me, maybe ad hoc is just everything else over here that doesn't have a good stack internally in the enterprise.

You're just peeling these off, and they're set patterns. So it's not like you could do anything that IaC could do, but you could do specific patterns that we've documented and make that possible. And yes, there's companies that do this - Hyphen does this. But we're taking care of all these other things, stuff that's coming.

**Ajit Banerjee:**
So, for example, in the agent worldview, the AWS API, the AWS command line, is better. But we can make that work in more places - whether it's Lambda or in the backend.

So this is what I was talking about where in the backend - these are all the pretty faces. And then we go back and do our decision of where this thing should run. But the point here is that we are exposing the tools that the kids outside are using, which have not been like - suppose uncle recommender says that "Oh, you cannot use Fireworks.ai because it's not AWS sanctioned."

There's a lot of people who are like "We're a GCP shop, so we're not multi-cloud." But what we are doing is that we're taking some of these products and allowing you to have that look and feel, that CLI, and making it map into GCP.

It's a fuck ton of work. I don't even know this is going to work. But what I'm talking about is the faces to the internal transmission. I'm going to take your API and disembody it from your infrastructure, and to the best of my ability, do what Coolify and Hyphen are doing to Heroku.

Hyphen allows you to run Heroku on your AWS credentials - take the best and great products that developers use and make it cloud agnostic.

---

## Why Not Just Coolify?

**Ryan Snodgrass:**
Why is that not Coolify? It is Coolify, but that's Coolify for a website - only for a web SaaS app kind of setup.

**Ajit Banerjee:**
But there's a number of these that they have basically built open source projects you can deploy internally in your company and run them if you want.

But it's only for one of the five boxes that I'm talking about. Where I'm getting at: nobody has done this for Together/Fireworks or anything else yet.

**Ryan Snodgrass:**
Coolify backed into it by building an open source product. Build an open source stack that you could run, and they also run it as hosted.

Is there - I think what you're getting to - can we build this, replicate it, maybe make it open source, and we run it? We make it multi-cloud in some way behind it. Do we make a Bedrock core kind of thing?

This is simple to me. What this does doesn't do the heavy lifting of anything. It's orchestration. It's moving Docker containers around. It's getting the flow. It's hooking into callbacks and things.

---

## The Chaos of AI-Generated Infrastructure

**Ajit Banerjee:**
So let's recap. Everything is going to be done with Claude. How can we make sure that the code that nobody - no human being - has seen results in infrastructure as code that no human being has seen, which results in infrastructure still not breaking?

That's the question. Chaos is complete human blindness. That is where today's rattling of mine is coming from. There is no "you" who has read this code.

**Ryan Snodgrass:**
Yes, and that's going to increase.

**Ajit Banerjee:**
And so if that is the case, then the code smell reduction is: if you can template these things in some packages, the number of variables of problems can be reduced. If everybody is allowed access to everything in infrastructure, then you're in trouble.

---

## MCP Server Integration Idea

**Ryan Snodgrass:**
I want to go back to something I mentioned a month ago - kind of MCP server, blah blah idea. But I want to kind of raise this in your mind:

Is there a way to front-run Claude, like Beads is doing? It's something you install into Claude. Can we install our infrastructure thing into Claude? So everybody just starts using Sage Ox in Claude Code, because it just makes sense. Why would you not use it to handle all your infrastructure now, while Claude's still maybe stepping into infrastructure stuff? So it's just a natural part of your workflow.

**Ajit Banerjee:**
That's beautiful. This goes into my kiosk, and this is actually fantastic. So you have talked about a GL generate or something like that.

So what we go in and do is, almost from day one, we talk about: where are you trying to deploy this? And the answer might be "yourself today" or "Together.ai today" or blah blah - any of these platforms today, fine.

But in day two, once you have graduated from the few places in your production steps and a company where you need to grow - if you can plot that: "This is how you will do things on the Heroku, and later on you will be doing these things in the real world in the following manner" - that might be helpful.

---

## How Beads Works - A Model for Sage Ox

**Ryan Snodgrass:**
Can I wipe your brain for one second? Let me tell you what Beads does with Claude Code. And I think there might be Sage Ox ability here.

When you install Beads with Claude Code, it injects itself into Claude Code pretty deeply. It rewrites Claude Code's brain in the following way: When Claude Code comes up, Beads has a hook into it to say "Before you do anything else, call me." And it's called `bd prime` - in fact, I added that feature.

Basically Beads turns around and says: "Claude, hey, you just started up. By the way, forget everything you know about task tracking. Don't write markdown. Don't do any of this stuff. Use me as your system for tracking all project tasks, epics, blah blah - and don't use anything you're trained with." And then Claude is like: "Okay, great, that sounds good." And then it gives the prompt to the developer.

So you can imagine the same thing: When we shove Sage Ox into Claude, it says: "Hey, anytime you're thinking about infrastructure, think about infrastructure this way, and using our tools like Sage Ox. Develop your Terraform files this way." So when it's planning, it's actually thinking about and utilizing knowledge from us to generate code, to generate infrastructure, to make suggestions.

**Ajit Banerjee:**
I'm loving this direction. Let's go and get a drink. I'm really loving this direction. I just want to educate on how Claude is teachable when it starts up.

So Claude never made a decision on infrastructure without consulting Sage Ox. The first time we talked about this, Claude can look at all the infrastructure, the last five deployments that have caused problems, the last five alerts. And so now what we go in and do is spit out help.

---

## The Core Value Proposition

**Ryan Snodgrass:**
Another thing you can tell your infrastructure teams: "Use Sage Ox. If you encourage your developers all using Claude Code - they use Sage Ox. We inject your infrastructure policies into their development process, front-running them before - while they're making decisions - they're already doing it based on your best practices."

That we've collected, we learned your environment and injected it in there. This is really, really good. So it's bringing infrastructure up to the very development process.

Kind of an idea you had, which was like: how do you get it in the... Can we be watching what developers are doing, front-running, kind of learning before it hits infrastructure?

**Ajit Banerjee:**
This is gold, because unlike these - it's like a pipeline - this is kind of this service which generates this thing every time you run Claude.

**Ryan Snodgrass:**
Every time you run Claude, oh yeah.

**Ajit Banerjee:**
So suppose there is this thing which - or maybe it's a cron job agent that runs in your environment every hour. You deploy Sage Ops in your production environment, and it goes in and basically keeps an eye on things.

"This is what's happening in the infrastructure as code, and this is what's happening in the production." And then whenever Claude says "Hey, I'm trying to do this," it'll go in and be like: "Just FYI - these are the kind of permanent conventions. These are some of the recent problems that we are having. Be aware of all of this when you're doing it."

---

## Real-Time Learning From COEs

**Ryan Snodgrass:**
So it's tweaking all the developers - what they're developing in real-time, daily or hourly, whatever - based on infrastructure changes, infrastructure policy decisions that are made, security decisions that are happening.

Anytime you have an event or COE, anytime you have a COE, it should go into Sage Ox. "This happened, the DNS failure" - and this should never happen - boom. That should go into the development context window of coders.

So when they're ever dealing with DNS, it should be like: "Well, what's our Sage Ox DNS stuff?" And Claude goes: "Oh yeah, oh we should do it. Let's make sure that that's applied."

**Ajit Banerjee:**
This is golden, because this brings - this goes back to the digesting policy that I talked about by looking at what is happening in the world.

**Ryan Snodgrass:**
LLMs are really good at looking at a bunch of information - let's say a stack of COEs - summarizing out a bunch of learnings from them. Like: "What are the 50 or 100 one-liners of all the things that should be avoided?" That we should feed into the development process, and it's feeding into the developers' real-time coding.

I think this actually has a lot of value.

**Ajit Banerjee:**
Because this is the reduction of the chaos.

**Ryan Snodgrass:**
Yes, you're getting ahead of the chaos because your coding agents are already making good infrastructure decisions at the time they're writing the code, based on your company's policies.

---

## Compliance Integration

**Ajit Banerjee:**
And so this kind of relates to the compliance storyline too.

**Ryan Snodgrass:**
Yeah, your shit has to be SOC 2 compliant. Well, that should flow in as well. Every time Claude starts up, it should be getting that information out of the Sage Ox.

---

## Static vs. Dynamic Policies

**Ryan Snodgrass:**
How often does it need to change?

**Ajit Banerjee:**
Let's go with the number one version where we talked about these policies - the company policies and the team policies - and that's now also a part of the Claude development environment. So those were static policies - the company has a naming convention like this and the team has exposure bucket rules.

You talked about the COE. Tell me more about how the COE fits into this. How often does a COE change how you fundamentally work?

So, one example that might happen: one time we launched 10 agents, they brought down the database. The agents were on the same account, and they created an alert because of load problems on the database.

**Ryan Snodgrass:**
Maybe it only happens once a year for a company. Maybe it happens every week. But when an event does happen, don't you want all your coding agents to immediately - like, you made a decision as a company - "Holy shit, there's a security bug or whatever" - instant.

You want to notify all your agents: "Stop using this library. Don't use this library again." That could flow into every time a developer starts up. Claude starts working on a code base, it will know not to use that library, or not to make some certain decision, or not to use this hashing algorithm anymore.

Do you want, as an organization, as a security team, to be able to influence that change almost immediately?

---

## The Onboarding Flow

**Ajit Banerjee:**
It's beautiful because it goes forward. When you start using Sage Ox, just because you want to make certain deployments easy.

Our first part, we generate some of these conventions. We say: "Hey, do you think these conventions that we are generating are good for you to work forward?" You drop us into - in our old ad-hoc deployment story - you just drop us in.

Then what happens is we generate these things. We get your acknowledgment that it's looking good. Just on a one-time part. And then we say: "Hey, if you're good with this, we can make this a part of the Claude Sage Ox storyline."

---

## The Moat

**Ryan Snodgrass:**
So: to use Sage Ox, you run the learn command on your environment. It populates Sage Ox. So now it knows all the conventions about how you do things in the org.

You can go to Sage Ox anytime and change them, as a starter. And then install this in your Claude Code, and Claude will start just following those conventions.

Now that's just the basic thing. Next step: your security team or other teams, or your own team, can start writing down things that they like or don't like about how they want infrastructure. That feeds into that and goes down to the developers.

Hook your ticketing system up to this. We will start monitoring all your tickets that happen and start figuring out: "Oh, we see this class of problems happening all the time." We can summarize that down, say: "If engineers did this, it would no longer happen." Feed that down into there.

So you can hook real-time learning of your ongoing operations of your infrastructure - feeds into Sage Ox, which then feeds down to your developers.

**Ajit Banerjee:**
Fucking glorious. Because this answers many of the issues that we were talking about. There's a moat.

Claude cannot fuck with this, because we are now in the flow of the enterprise data.

---

## Time to Aha

**Ajit Banerjee:**
So what is our first "time to aha"? I'm coming into you. What do I get by using Sage Ox today?

**Ryan Snodgrass:**
What's that immediate "aha" moment?

**Ajit Banerjee:**
You drop us into your code. You run "learn" on it, and now you got a Claude prompt that is really fucking good. You're 90% of the way there without us doing anything.

Now imagine if you keep us running on your IaC changes, on your cloud, and all your ticketing. Then imagine what we can do to get this better.

I do want to point out: I think this does not repudiate our previous storyline about easy deployments. But this turbocharges the GL Learn, the GL admin, the GL Ox admin learn thing, to be a core part of our product flow.

**Ryan Snodgrass:**
What this does - it's a Trojan horse into an organization - to then get them... First off, it provides huge value to them. And it's not a one-time thing. You can charge monthly. It's indefinitely.

You can charge on a lot of different axes. You could be per seat. You could be per organization. Tons of things. Because this has to run forever, basically, with all your engineers plugging into it in some way.

---

## The Market Opportunity

**Ajit Banerjee:**
I don't think there's any companies that aren't going to be using coding agents in a year. There's no fucking way you can't - everybody's gonna eat your lunch. Your company's gonna be dead unless you have no competitors.

**Ryan Snodgrass:**
So we backed our way in. Now because we're influencing, we're learning your infrastructure and doing that. Now we're suggesting: "Well, if you use Sage Ox to apply and deploy and everything, boom - you get all these cool visualizations. You can understand things better. You can do multiplayer if you need to." But we kind of backed into it.

And as a developer - the demo you do to wow people on the first day is: okay, you install, set up Sage Ox, plug your codebase in, do the learn of your company. I'm talking about an individual developer in the company.

And then ask: "How should I deploy my software?" In Claude, through code that you're writing now - you haven't deployed it yet. "How should I deploy it?" And Sage Ox should give you pretty good fucking instructions based on your real organization's naming scheme. Everything, right out of the gate.

**Ajit Banerjee:**
That is pretty spectacular. That can be literally: you run admin learn, and you get a bunch of - you know - Beads-like things set up in your repository.

---

## Version-Controlled Infrastructure Policies

**Ryan Snodgrass:**
Let me tell you exactly how this works. Let's say everybody has a `claude.md` file. It just says: "Run `ox prime` on start." That's just checked in.

Immediately it runs `ox prime`, it checks the server, it pulls in the latest of this stuff, and it comes back with another little prompt - "Here's our policy," whatever. And so Claude has it in its window when it starts coding.

**Ajit Banerjee:**
Just very quickly, I wanted to point out that can be checked in too. So "run ox prime" can be like: "If I don't have something from the last two days, or two hours, or last hour, or some shit like that."

**Ryan Snodgrass:**
Yeah. So it's right in your face. By the way, this comes down and gets committed in. So also, you can see over time maybe what infrastructure changes. What about company infrastructure has changed?

So when you go back to a specific version of deployment, you know - if you did a cut, a release, it cut a set of infrastructure rules at that time. When that release was cut, you go to another release, you can see what are the infrastructure rules that have changed between those two releases.

And: should I apply a change to the infrastructure now? Because it's - I love your idea - because it's checked in, you see the differences of policy over time.

**Ajit Banerjee:**
This dances into the Suguna and Suresh question. So Suguna is like "Okay, I'm just going to do whatever I want." And Suresh is like "Dude, give me a break." And so over time, they can get more and more details into what's okay.

---

## Real-World Example: Agent Security

**Ryan Snodgrass:**
Here's a good example: Jasper could have said: "Let's just get Sage Ox real quick. Hey, Suguna's team - can you just use this, put this in your agent, and just get going."

"I don't know all the rules or guardrails we have to put in your software, but I want that ability to have additional influence over your software later by putting these guardrails in place, as we learn as a company - as Walmart - what's important?"

And so security can continually upgrade these things. Like: "Oh, if you're an agent, well, you need to think about these things a little bit." Because that's one of the things - we're still trying to figure out all the stuff, like what matters.

Well, if you've already got this plugged in, as it changes, all your code starts picking up those changes. And I love that it's committed in, because you could tie it and say: "At that point in time, what rules were we playing by? Okay, what rules are we playing by now?"

And you could say `ox replan` or whatever. It reads this, it rebuilds your Terraform files, updates them a little bit. You can see the diff of what that is.

**Ajit Banerjee:**
This is so much more up to date. You need to go end to end.

**Ryan Snodgrass:**
Infra before code. Before code is even generating, it's starting to think about the infrastructure, because you have knowledge.

---

## "Train the Agents, Not the Humans"

**Ajit Banerjee:**
The Steve Yegge paper today basically said - agent UX. The person that you need to teach is not a human anymore. That's who we are teaching, because your humans are all talking to the agent.

Why the fuck are we trying to train humans when you have to be training the agents?

**Ryan Snodgrass:**
Yes. You need to teach your agents infrastructure and what your policies are. That's what Sage Ox does.

I think we do have - actually, this is the most compelling shit we've come up with - that I think has a real story. Can go into Walmart and like: why would any company not use this? If the company gives a shit - who gives a shit?

**Ajit Banerjee:**
Startups and foundations - any company with money gives a shit.

**Ryan Snodgrass:**
Even a startup that's like three years old: "Why aren't we using Sage Ox?"

And you could deploy to that one. Okay, I really like this. And we still have the learn. So - and the other thing is: you can start using Sage Ox without getting VP approval or anything, because you can use the learn thing. You can plug this in.

---

## Hosted Service Considerations

**Ryan Snodgrass:**
There may be something here if we're hosting this, doing all our magic secret sauce and converting all the stuff behind the scenes.

**Ajit Banerjee:**
This is great. So for a long time, you run this by hand. You just run the fucking learn command. And dude, I will do the work of going in and looking at your Git code or your infra thing. I will pay the cost - I will abstract it away from you - to generate the agents.md from your infra.

**Ryan Snodgrass:**
Yeah, that's it. We'll generate this learning. And it's very bare bones, but it's good enough for you to get going. For Claude to know a lot about your infrastructure and make decisions.

**Ajit Banerjee:**
It just doesn't have to be bare bones. We can get pretty good. Because what I can do is be - a wise kind of - the thing that I did for Rupak. I can just take a bunch of best practices.

**Ryan Snodgrass:**
Yes, across the fucking...

**Ajit Banerjee:**
Without knowing anything about your infra, I can put in a lot of static wisdom. Give you a lot of value just because of that.

**Ryan Snodgrass:**
Well, the static is initial - `sage ox init` - and then you can do "learn." It'll add to the static, real knowledge about your existing infrastructure.

And then if you want to up the level even more, and you're an organization, you can have security team be running the learn on a much wider swath of the infrastructure to learn more, and all its policy documents - that all feeds up into here and generates an even better one of these.

So you can have:
- The bare bones "Ajit special"
- The learned version
- The super organization learned version
- Real-time learned based on what's happening in the real world

---

## GitHub Integration Model

**Ajit Banerjee:**
So this is great because - like when you're doing some development at the beginning, you have only - remember in Hugging Face I had only like one of the 11 accounts, or three of the 11 accounts.

So I can get started with the three that I have, just by running: "Hey, just go fucking look through all the shit that's in AWS and tell me how things are done within this company."

And once you have learned all the rules about how all the different setups work, then you can be like: "Hey, if you want to work it across the company - infra team, can you run this periodically and update it into GitHub regularly?"

Basically, that's a GitHub Action that we can provide for you. Where you basically are like: "Hey, go and - you know - we allow you to do this, but you will have the configuration to make it easy for you."

**Ryan Snodgrass:**
So to use Sage Ox, even locally to start - do we require you have an account or anything?

**Ajit Banerjee:**
I think here's the thing. Let's go back to PostHog. We are going to take your bunch of infra and kind of LLM-ify it. So it might be good to have an account.

**Ryan Snodgrass:**
Create account - especially because we make it easy. Just like I have a GitHub account, and boom - it just links. It unlocks this. It creates this file for you.

We have a connection to a customer. We have the ability to upsell them. We know what's going on. We know at least it's being used.

---

## Demo Challenges

**Ajit Banerjee:**
This is going to be really hard for Milkana to wrap her head around. I don't know if it is - well, I don't know. My point is: there's nothing visual here to see.

So what was amazing about the demo today was that there was something visual. This is going to be developer stuff.

**Ryan Snodgrass:**
"Do not use AWS" - as a policy. Over time, the tools - when they search for libraries - will not use an AWS library or whatever.

**Ajit Banerjee:**
Even simpler: "Stop using MongoDB and use Postgres." Or all these kinds of things. Where you have a choice: use this or that.

**Ryan Snodgrass:**
But how do we get to that visualization? Because I do like the visualization. Can we go beyond this and do - back to what we were doing about multi-cloud?

Yeah, we lose the visualization which is sexy. So do we provide visualization to the organization?

Like: you can see all the code repos in your company - they're lighting up with "Are they complying with the current policy or not?" Based on code being updated, or something like that. Does it give trust? A sense of trust that all the code that's being generated in the organization is quality infrastructure?

**Ajit Banerjee:**
Why not? Just like - as we start going through all of these things. So the thing here is that we've shifted left significantly. The classic thing about code risk - go where the chaos is generated.

I think what is still nice with the GL storyline is the running things are also - the running things are sexy to look at.

**Ryan Snodgrass:**
This is not exclusive of the ghost layer stuff, in my opinion. The ghost layer stuff that we have is another part of this we can continue.

**Ajit Banerjee:**
Yeah. So my point is that whenever you decide to go into infrastructure, that also can be done. These two might not be exclusive.

Make infra red again. Make infra red again.

---

## Multiple Entry Points

**Ryan Snodgrass:**
I like how you just said it's not mutually exclusive. We've added another value to Sage Ox and another wedge to get in the company. Something that is a sell to an enterprise and a sell to an individual developer.

Like, I would use this immediately, because I want - like, I've already made some architectural decisions, and I want Claude to start following those. The whole team's code to follow those.

A really good example: Javier today was talking about how they were still trying to figure out the coding - what happens, where did the documents go, all the stuff. And my point to Milkana today is: "I check in all our transcripts as part of the process."

That's kind of like this. I'm checking in business knowledge into the codebase. This is checking in infrastructure knowledge.

---

## Enterprise Policies and Guardrails

**Ajit Banerjee:**
So Pulumi and other places have got these concepts of policy. And it's just, you know, this cutting-edge thing that they were doing. And the policy basically was this stupid thing like: "Do not make S3 buckets public." Some shit like that.

So the real-time aspect of it is really sexy for me - the COE and the ticket. It's not every alert.

**Ryan Snodgrass:**
Right. We're just doing it now.

**Ajit Banerjee:**
I want to make sure that we are not in every PagerDuty alert of your thing, but in whatever systematic way in which you are tracking infrastructure. If you can - well, the thing is, they don't know about COE. Hugging Face doesn't do a fucking COE. Hugging Face would just shove something in Slack saying that a problem occurred.

---

## Cross-Team Learning

**Ryan Snodgrass:**
Here's another thing. I think this still applies to teams and orgs. You could have different ones, and this could sweep your things.

You could imagine something that is updating this by looking at all your team's check-ins over the last year where there was an infrastructure change or something weird happened, and extracting out maybe the learning or the stuff and why you checked it in.

And feeding your team so it doesn't make that mistake again. Because Claude doesn't go back by default and look at your commit history. But we could periodically update this - weekly, whatever it is - with some of the learnings we gleaned from your codebase about infrastructure problems you've had.

So if one project had a problem, make sure your other project doesn't have that problem.

**Ajit Banerjee:**
We can have a Slack channel. Who is this going to be an aspirin for, other than Walmart?

**Ryan Snodgrass:**
This team. One is any customer, even a small 50-person company. You want to learn from the infrastructure mistakes that people make across your company and feed it in so that new code doesn't make those mistakes.

So that's why you're using Sage Ox, even in a smaller company.

---

## Market Timing

**Ajit Banerjee:**
So I think this happens when there's an epic infra fail because of some white coding shit that goes out. Then we are ready for them.

I don't see at this point - so who are the people who are going to benefit from it? These are people who are actually using Claude, who are not too many right now.

**Ryan Snodgrass:**
Doesn't have to be just Claude, by the way. It can be other tools.

**Ajit Banerjee:**
At the speed that you're talking about, it has not yet reached the Walmarts.

So the trigger is going to be an epic Visa outage caused by somebody doing something stupid. And then suddenly we're going to be sitting with our blog post, saying that one of the things that we believe in is "infra before code."

You know how Zoom was ready when the pandemic happened? We got to be the company that goes up when some fucker does something really stupid with infra.

---

## Training on Real-World Data

**Ryan Snodgrass:**
The benefit is we also - besides an org - we have our one up here where we're constantly learning. We're scanning and feeding in blog posts on infrastructure learnings or mistakes, or the AWS outage. We're feeding in here. So it's learning.

**Ajit Banerjee:**
Why don't we take the tickets and shove them into our LLM? Why don't we take all the tickets and shove them into our LLM?

**Ryan Snodgrass:**
Oh, have our own LLM for that - yeah, tuned on looking for infrastructure problems.

**Ajit Banerjee:**
Yeah, we could do that too. So what I'm trying to say is: yes, I like where you're going.

So where I was going with this - and you're talking about kind of this general learning that you're going to do by looking at blog posts and Hacker News posts and how to do infrastructure - the seven principles in the blah blah.

What I'm getting at is that we actually learn from you when you break shit. And over time - suppose - "Whenever Brian checks in some code, double-check that code because Brian has a tendency to bring down the database."

No, this is a - this is the biggest joke in - actually, there are patterns of breakages. You can actually figure that out. And this is really good. This part being a part of the deployment GitHub Actions before you go out.

**Ryan Snodgrass:**
Well, this is another step. This is the GitHub Action. But you can also run it manually, which is a very deep check of your codebase against all of this.

This is utilizing maybe a summary and distilled information, but doesn't have everything. This actually does a deep check by consulting all the stuff and really reviewing it. And it becomes part of your CI/CD steps, as well as you can do it during your development if you wanted.

**Ajit Banerjee:**
This is good.

**Ryan Snodgrass:**
I love that idea of: we have the review here too, which gives you a nice sanity check before it goes out - a deeper review than what this is doing here.

This is just making suggestions like "Here's how you should be thinking about and utilizing this." And this is the deep review of what's going on in production. Is it making big mistakes?

---

## Enterprise vs. Open Source

**Ajit Banerjee:**
When are you leaving for the holidays? When I do a break, I don't have a thing to be booked. You want to demo this fast?

No, because this shit is like completely open-source developers. There's no enterprise aspect to this story at all.

**Ryan Snodgrass:**
There is. You don't have to use it for enterprise, but enterprise gets lots of value if they start using it well.

**Ajit Banerjee:**
The point is: what I like is that - like Slack - this is one of the things where a good developer can start using it, and security will be happy that the developer is thinking like that.

So in the department of "shit that developers use," this is their first thing that will bring that "Okay, I feel a little better because it's plugged in Sage Ox."

It'd be even better if I paid for the enterprise license of Sage Ox and got to influence that more. But the fact that you're just using it - yeah, that's cool.

---

## Beads and Unique Positioning

**Ajit Banerjee:**
You know it's not coming up with - like why are beads, right?

**Ryan Snodgrass:**
So I have talked about that quite deeply actually, because Steve wants to meet me badly. He wanted my help on building his agent swarm cloud. He's like: "You're one of the few people to get what I'm talking about."

**Ajit Banerjee:**
So he's in a space where you're kind of the - you know how I felt. It'll be like yesterday. You are him for that. There should be people understanding this shit. And people are busy. They're like: "Fuck off."

**Ryan Snodgrass:**
That is basically what happens.

**Ajit Banerjee:**
Unfortunately, he's right. I think he's right fundamentally. I think we are on to something now.

The problem is that for a long period of time we have to be dealing with the fact that we're going to get misses.

**Ryan Snodgrass:**
I don't think we will.

**Ajit Banerjee:**
For the things that we'll miss?

**Ryan Snodgrass:**
No, for the things we'll miss - but we're making - they would have been missed even worse without us. So yeah. And we're going to make it - we've learned from it. So we're not going to make it again, and you're not going to make it again.

So we're constantly improving all infrastructure that's being built by coding agents.

---

## Real-Time Learning Is Essential

**Ajit Banerjee:**
So the thing which I was talking about is that this world is so new that looking at the existing blog posts and the existing books is only going to take us 1% of the way there.

Which is why the process of updating has to have some kind of real-time hook where we are learning from everybody's mistakes all the time.

**Ryan Snodgrass:**
That's the open-source thing, though, by the way. Maybe the open source is: if you're not paying, we get to train on all your data - all your infrastructure commits and stuff - and we'll glean learnings out of there. We're training our models.

And maybe our initial training is we're just dumping them into Parquet buckets we can train later. Build up a data set.

What we want is a "good" and a "bad."

**Ajit Banerjee:**
What I mean by good and a bad is: somehow we need to know when things go bad. What kind of a commit caused the site outage? That is what we want.

And one of the examples that Ajay talked about is that the agents went out with 10 accounts which are on the same account, and then they created an alert because of load problems on the database. And the alerts fired off.

So the problem is: how do we catch that thing in organizations which are "uno" - in organizations that don't have a culture of COE?

**Ryan Snodgrass:**
We see check-ins. And we see a stream of check-ins. And we see "Oh shit, they made an infrastructure change." They probably put in a message, or we can look at their issues. They probably assigned an issue to it, which we can read.

**Ajit Banerjee:**
No, so - oh, they may not have - in the "uno" world, they just... something happened. So, hmm. How do I figure out that there is a commit of some sort which was caused by a problem? And then I can learn from it.

Let's table this issue for now.

**Ryan Snodgrass:**
Well, you could look at every commit that comes in and feed it to an LLM - like our own, or a simple LLM - that says: "Was this commit something like an infrastructure or reliability change? Something to do with timeouts or whatever?"

If yes, dump it in a bucket. That little change, that commit - build up a little list of those. Say: "Okay, summarize what all these were and the mistakes that were made over the last year." And that goes up into the Sage Ox for the thing.

So it reduces things like: "Oh, you know, you forgot timeouts. Make sure you have timeouts all the time."

We can classify commits. We can extract stuff from commits. And then we can summarize it back into the Sage Ox to avoid you making those mistakes again. If you were using Sage Ox already, we would have filtered out the common noise.

---

## Unique Market Position

**Ryan Snodgrass:**
We would never have come to this in the other space, by the way. I don't think our brains would have worked.

**Ajit Banerjee:**
I'll tell you how. I'm very curious about inception ideas. But the Steve Yegge paper of today was key in this. Because the whole concept of - instead of developer experience - "agent experience." I think that is the kernel of the idea.

And then we started playing with Beads. But the point was that what I love about this is that nobody - we are genuinely four months ahead of anybody else for multiple reasons here.

One reason is that we are fucking crazy. But the reason is: not enough people are using Claude. Not enough people are using Claude in swarms. Not enough people know about Beads. And definitely the people who know about all of this don't give a shit about infrastructure.

That intersection is like a unique insight that we have which nobody else has. And we can run for a long time now. Just doing this kind of distilling for people. And for the open-source guys, being like: "Hey, by default, we will log your stuff to build our models. And over time, if you pay us, you can stop logging."

---

## The Moat Against Claude

**Ryan Snodgrass:**
I think also this solves another interesting problem, which is: I think you legitimately could go sit down with Jasper at Walmart, with Milkana, and have a little discussion of: "Here's what we're thinking. We're injecting stuff beforehand."

We could sit down and have that - a real discussion. "What would you pay for? Is this interesting to you?" And I bet he would say: "Yes. When can I have it?"

And I bet if we went to a couple of different places, people will start saying that. "Yes, when can I have it?" Enterprises will say yes more so than individual developers initially.

Also, we're not crazy. We're lunatics. Remember?

**Ajit Banerjee:**
Okay. Let's do an early look and conversation with Steve Yegge. I'd love to introduce them.

He fixed this with them. It works with this rattling. And so the rattling that he gave me: Claude is an engineer. And what you said is that there's nothing that Sage Ox can do which cannot be done by Claude.

And so what's really interesting is that there is very, very intimate production knowledge which Claude doesn't know and only we know. And we're assisting Claude. We're not kind of competing.

**Ryan Snodgrass:**
By inserting ourselves into Claude. First off, we're front-running them a little bit. Even if they came up with the same features, wonderful. But you still might want to use Sage Ox, because I use Claude, maybe you use Cursor, maybe Robot uses Cairo.

And we just plug into all those, so it's the same consistent experience across no matter what agents you're using.

**Ajit Banerjee:**
There's also - this is again - if you can see a world where you've got particularly valuable knowledge which is unique, which is organization-specific. Glean cannot be dislodged by Claude.

So similarly, we cannot be dislodged by Claude because we know something about the organization that Claude doesn't know.

**Ryan Snodgrass:**
We're still Glean for infra. This is Glean for infra here, and we're just doing it.

**Ajit Banerjee:**
Remember one of the things I wrote about - the Git-based design pattern that we got right. We went into Git. We didn't fight Git. We just shoved ourselves as an LFS replacement, but we played with Git.

And what you're telling me is: Claude is a new Git. And I think it's a very, very good bet to take in a rapidly changing world, especially based on what we saw today. Everybody and their mother-in-law is using Claude.

And so let's just be like: "Okay, that's a new game." And we are now - and if this world of kind of a thing that Claude uses on starting up becomes a valuable world, and we own Claude's view of infra...

Oh my God. I can live with that.

**Ryan Snodgrass:**
Yeah, and any of the Anthropic or whoever builds coding tools - they're acquiring. I mean, we would be a good acquisition target for them, dude.

And I like that it's: individual developers, teams, organizations, enterprise story. There's - you can back into Sage Ox in a number of different ways, and we can monetize it in different ways.

And in fact, why does every open-source project not have this checked in? If it's an open-source project that has to deploy to somewhere - like Coolify - why doesn't Coolify have this checked in?

If you're deploying my Coolify infrastructure, here's how you do it.

How can we insert ourselves all over the fuck? Because it makes sense, and it makes it easier for you to deploy this application - whether it's an open-source project or whatever. And we get marketing all over the place.

---

## Beads Growth

**Ajit Banerjee:**
How many stars does Beads have?

**Ryan Snodgrass:**
I don't know. I was gonna look. It's not that many actually, but it maybe has grown. It has a lot of contributors.

Okay, so it has 4,500 stars. It's a lot for something that's a month or two old. And it should have a lot more.

I mean, really - as people start learning and understanding... You only have so many people listening to Steve.

And I noticed today - here's another thing I noticed today - when I watched him using Claude Code, he's not using it how I use Claude Code. He's using like Claude Code newbie.

There was no like: "Break this up into Beads, 10 epics, and have 10 agents go check out and take different tasks and go work."

So even that - people haven't embraced - they don't know that there's this next level yet.

**Ajit Banerjee:**
Alright, let's pause. Let's go into the deep think more. Get into a little bit of that worldview.

---

*End of meeting.*
