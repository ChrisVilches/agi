**NOTE:** This file is a template written by the user (human), don't modify it. It's meant to be a human-friendly
summary of the whole application, focusing on parts the human user is interested in, and discarding things that
aren't very important. The agent (LLM) is supposed to fill in the gaps and create an actual document from this template.
Don't add overly detailed explanations.

## Agent (general)

When does a loop end?: 

## Programming

(Instructions: if anything was removed or deprecated, keep the question, but simply say it was deprecated)

What's the concept of "registry" used in the app?:

## Interruptions

How can a run be interrupted?:

(Instructions: for each of the following questions answer what message is
appended to the conversation, and how is it restored.  If the answer is the
same for all, you can summarize as you which.)

What happens when a bash tool call is interrupted? (for example a long `sleep`)

What happens when a fork is interrupted? 

What happens when a subagent is interrupted? 

What kind of processes spawned by agi are likely to get stuck and not get
killed properly by agi (if any) when doing ctrl-c (graceful shutdown)?

## Native tools

List all the native tools (without explaining).

When does an agent, subagent, fork (or any variant) get or not get any native tool?

How are native tools explained in the prompts? in the system prompts? or simply in the tool array passed to the model provider?

Can they be disabled by the user?

## When a fork is on-going

What native tools can be executed?:

What skills can be executed?:

After a run with a fork finished, a snapshot is created, but when restoring it, can we see the history inside the fork?:

Up until what part of the conversation would be KV cached when starting a fork? Is the whole conversation up until the
fork creation, or are there some differences?

## When a subagent is spawned, what does it inherit?

The system prompt is: (How does it differ from the parent system prompt?)

The workspace tools are: (same as parent? some change? some are added?)

The user defined tools are inherit in what way:

The skills available are: (how are they passed from the main agent?)

## Terminal Tool

What happens if an agent opens a terminal (gets ID 1), and then forks (without closing the terminal first).
Can that fork access terminal ID=1? It's in the conversation, so it will see it as open.
If not, is it because "each agent (main, subagent, fork agent, etc) gets their own list of terminals, starting from ID=1 each)?
In that case, the fork will get an error when trying to interact with terminal ID=1 because in reality it doesn't exist for
that fork, right?
What kind of error does it get?
That fork agent can open a terminal and it will get also ID=1 right?
