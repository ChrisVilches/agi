#!/usr/bin/env bash
#
# A MessageDisplay hook that renders what the model wrote for the reader.
#
# The model writes markdown and agi prints it raw. This hands the text to
# glow and hands back what glow drew, so headings, bullets and fenced code
# arrive on the screen looking like what they are.
#
# It sees the prose and never the deliberation: a display hook renders one of
# the two streams, and the one it renders is the one it did not ask for with
# "thinking". thinking.sh beside this is the other half.
#
# This is an example and it sits under examples/ because that is what it is:
# a hook written for daily use and kept because it is useful, not a program
# agi ships. It is held to none of agi's standards. Copy it and change it —
# the whole of it is below, and there is not much of it.
#
# Whether glow and jq are installed is this script's business and not agi's.
# agi runs the program named in hooks.json and reads what comes back; a
# renderer that is missing, that hangs, or that exits badly leaves the message
# printed as the model wrote it and tells you what went wrong. The `|| printf`
# below is the same courtesy one step earlier: a glow that cannot draw this
# particular text gives back the markdown rather than nothing.

set -euo pipefail

content=$(jq -r '.delta // empty')
[ -z "$content" ] && exit 0

# -s dark names one of glow's built-in styles, where thinking.sh names a file.
# The asymmetry is deliberate: a file exists in this folder only where a
# built-in name could not say what was wanted, and here one can.
#
# -w 0 disables wrapping. The terminal is the thing that knows how wide it is,
# and text that flows survives a resize where text wrapped to the width it was
# written at does not.
rendered=$(printf '%s' "$content" | glow -s dark -w 0 2>/dev/null \
  || printf '%s' "$content")

jq -n --arg content "$rendered" \
  '{hookSpecificOutput: {hookEventName: "MessageDisplay", displayContent: $content}}'
