Transaction specification

There will be a control flow tool for creating transactions.

The LLM can create a transaction when it feels what it's going to do may be dangerous, and there should be a way to revert the work done.

The semantics will be: start transaction, rollback, and commit.

There won't be a "retry" semantic, since the LLM can just decide to start another transaction.

rollback will be "rollback with summary", where summary is a required parameter explaining everything done, and want went wrong, and optionally
explaining what can be attempted next.

commit should also have a summary, it acts in the same way as how subagents return only the final answer to the main agent, without explaining
everything done. However note that all the work done inside a transaction is shown on stdout when using TTY (unlike subagents, it's not hidden).

bottom line, both rollback and commit explain what happened in a similar structure as how subagents return a "final answer", with just
the difference that rollback and commit represent two different outcomes.

How to use the transactions should be explained in the system prompt, similar to other control flow tools.

A transaction cannot be started inside another transaction (e.g. when a transaction is on-going).
Subagents shouldn't be able to be created during active transactions since we can't revert what subagents did internally because
we don't have knowledge of what they are doing after they finish. The subagent spawning tool should return an error to the LLM
in the tool result message explaining that this behavior is invalid. It should also be explained in the system prompt.

Everytime a tool call is executed, the LLM must reason whether this tool is reversible, and if so, execute a new tool which we
will add, which has the purpose of registering two tools, one for cleanup and one for reverse operation, such as:

tool_call("register_transaction_reverse", parameters: { cleanup_tool: {...}, reverse_tool: {...} })

Where both cleanup_tool and reverse_tool contain the tool name to execute with their parameters. This gives us a JSON with
a lot of validations to do (and if they don't match our tool schema, we should return a message in the tool result saying that
the tool was wrongly formatted because it doesn't match our expected format or parameters). Only tools for doing work
should be registered with a reversion, i.e. don't reverse tools that create subagents since there's nothing to reverse and don't
try to reverse the tool that terminates execution.

All of this should be reasoned by the LLM, and this makes work inside a transaction more expensive.

Since it's the LLM the one that needs to come up with the reverse operation, and it may do mistakes, this reversal is "lousy" since
it can have leaky effects due to bad LLM reasoning.

cleanup_tool is to cleanup things created for rollback, or for protecting the previous state, such as backups.
These tools are executed for both rollback and commit. We need to explain in the system prompt that the LLM doesn't need
to manually cleanup things since the harness will execute these things manually.

reverse_tool: this tool registered is meant to do the opposite work as the one done inside the transaction when trying to do actual work.


obviously reverse should be executed first, and then cleanup, so that the reverse one can use the backups that cleanup tool
will remove afterwards.

These tools are executed by the harness automatically on rollback/commit.

some tools may not need a reversal, since some tools are read-only (such as doing cURL or reading a file).

Add this as a TODO in the documents and code: Some tools should have automatic reverse/cleanup, such as when we implement
a native Write/Edit file. We can backup the files ourselves, so we don't need to rely on the LLM to reason about how to reverse them.


both cleanup tool and reverse tool may register another tool (can be any, not just the same one as the initially executed when doing
work inside the transaction).


all of these tools registered for reversal are executed in the opposite order in which they were registered.

edge cases:
* termination control flow tool executed inside a transaction.
* graceful shutdown while a transaction is active.

NOTE: For all terminations where the transaction is active, we should rollback and cleanup automatically.



Summarization and truncation: As explained before, both rollback and commit summarize what was done and what happened, but this should
also truncate the message and leave the messages in a state such that when taking the snapshot or restoring the session, one can
see that the LLM first wanted to execute a transaction with a certain task, and then the next message contains the final answer,
in such a way the order and flow of messages doesn't look unnatural. Note that for this reason, we might need to
add title and body to the transaction request, so that it adds the context on what was requested, otherwise that information
would be lost, and further messages won't see that, due to the truncation. Here's one example on how to truncate:

before transaction is finished:
tool_request: start transaction with title: "backup this folder", body: "full prompt on how to accomplish this task..."
tool_result: the transaction for task "backup this folder" has been started
assistant message: more reasoning with further tool requests to do work and register cleanup/reversal
...
tool_request: commit transaction with message "this task was achieved and this happened..."

after transaction is finished (messages are truncated and re-arranged):
tool_request: start transaction with title: "backup this folder", body: "full prompt on how to accomplish this task..."
tool_result: the transaction for task "backup this folder" has been executed and finished correctly with the following result: this task was achieved and this happened...





questions:


what abstraction in the model can we use for "bad tool usage"? such as if the LLM tries to create a transaction inside a transaction
or a subagent inside a transaction?

is executing the reversal tasks in opposite order the most sensible thing to do? It's to do the reversal in the same order as the
registering history, but opposite to go in reverse time.


edge-case:
the LLM decides to give a final answer before committing a transaction. Should we do auto-commit? what are the implications? 
I'd like to have a way to easily resolve this scenario without adding extra complexity or speghetti branching. Maybe we treat
this as auto-commit and leave it like that, using that final answer as the commit message.

Are there any more edge cases?
