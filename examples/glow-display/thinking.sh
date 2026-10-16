#!/usr/bin/env bash
#
# A MessageDisplay hook that renders the deliberation a thinking model returns.
#
# It asks for that stream with "thinking": true in hooks.json, and so it sees
# the deliberation and nothing else — the prose written for the reader goes to
# message.sh beside this. Two hooks rather than one with a flag on it, which is
# how agi has it: a renderer armed for your answers is never spawned over the
# model talking to itself.
#
# This is an example and is held to none of agi's standards. See message.sh for
# what that means and for what happens when glow is not installed.

set -euo pipefail

content=$(jq -r '.delta // empty')
[ -z "$content" ] && exit 0

# agi stops formatting the deliberation the moment a thinking hook is armed,
# so the whole look is this script's now, and thinking.json is where it lives.
#
# It is dracula with the body text dimmed: document.color and list.color are
# #6272A4, the palette's own subdued color — the one it already uses for
# comments and for hr — while every accent keeps full strength, so a heading
# is still bold purple, **bold** still orange and `code` still green. Those
# two lines are the whole difference from upstream dracula, and they are the
# knob to turn if the prose reads too dark or not dark enough.
#
# It is a color and not the faint attribute because glamour has no faint to
# give: the `faint` field in a style file is accepted and silently ignored
# (`italic` emits its SGR and `faint` emits nothing), and an escape smuggled
# in through a prefix is closed by glamour's own reset before it reaches any
# text. Dimming glow's output from outside fails for the same reason — every
# color it paints ends in a reset, and a reset cancels an enclosing faint.
#
# The file sits beside this script rather than naming glow's built-in
# `dracula`, which exists and rendered the same thing before this: a file is
# pinned, so a glow upgrade that retunes the built-in cannot change what the
# deliberation looks like.
#
# A hook runs in the folder its hooks.json is in, which here is this folder,
# so "$here" is the same as "." today. It is written out anyway: that is what
# lets this folder be dropped whole into a larger --resources folder with the
# scripts a level down from the hooks.json that names them.
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)

# -w 0 disables wrapping. Deliberation is long and the terminal is the thing
# that knows how wide it is; text that flows survives a resize, where text
# wrapped to the width it was written at does not.
rendered=$(printf '%s' "$content" | glow -s "$here/thinking.json" -w 0 2>/dev/null \
  || printf '%s' "$content")

jq -n --arg content "$rendered" \
  '{hookSpecificOutput: {hookEventName: "MessageDisplay", displayContent: $content}}'
