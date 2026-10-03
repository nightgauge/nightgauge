# #2095's recording: guided repair on the #1955 permission scenario, where the
# GitHub App lacks Organization → Projects (doctor reports NGD036, a manual
# remedy). Recorded by
#
#   scripts/doctor-app-permission-walkthrough.sh --record
#
# in that state, which also confirms NGD036 is the only App finding before it
# records. Needs vhs 0.8 or later (for Wait). Run by hand, from an empty
# directory (the outputs land there), in that state:
#
#   NIGHTGAUGE_RECORD_CHECKOUT=/path/to/checkout vhs /path/to/doctor-guided-repair.tape
#
# The session shows live check progress and the summary, then the manual
# remedy's numbered steps and links; d shows the details, c re-runs the check
# (still present: the permission is still missing), and s ends the walk with
# the report. The link is never opened.

Output doctor-guided-repair.gif
Output doctor-guided-repair.mp4

Require nightgauge

Set Shell "bash"
Set FontSize 15
Set Width 1400
Set Height 900
Set Padding 20
Set TypingSpeed 40ms
Set WaitTimeout 120s

Hide
Type 'cd "$NIGHTGAUGE_RECORD_CHECKOUT" && clear'
Enter
Sleep 500ms
Show

Type "nightgauge doctor --only github_identity"
Sleep 500ms
Enter

# The manual remedy's prompt: its steps and links are on screen.
Wait+Screen /\[c\]heck again/
Sleep 4s

Type "d"
Sleep 5s

Type "c"
Wait+Screen /STILL PRESENT/
Sleep 4s

Type "s"
Wait+Screen /need you/
Sleep 5s
