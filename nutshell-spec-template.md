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
