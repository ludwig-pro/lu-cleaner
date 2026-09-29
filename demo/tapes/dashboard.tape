# dashboard.tape — `lu-cleaner`: scan (nothing is selected afterwards), browse
# the worktrees, smart-select the recommended project artifacts with `a`, add one
# more item with space, then clean, confirm, summary. Synthetic sandbox only:
# record with demo/record.sh dashboard.
# poster: 22s (frame used as the video poster by demo/record.sh)

Output site/public/demos/dashboard.gif
Output site/public/demos/dashboard.mp4

Source demo/tapes/settings.tape

Sleep 600ms
Type "lu-cleaner"
Sleep 400ms
Enter

# Scanners stream their results. When the scan ends nothing is selected:
# the status line says how to choose.
Wait+Screen@30s /Nothing is selected/
Sleep 4s

# Worktrees: the details panel follows the cursor (branch, status, why).
Type "g"
Enter
Sleep 2.5s
Down
Sleep 1.2s
Down
Sleep 2.5s
Down
Sleep 2.5s

# Back to the categories, then project artifacts.
Escape
Sleep 1.2s
Down
Sleep 600ms
Enter
Sleep 2s

# `a` is smart select: it picks the recommended items of this view (safe,
# regenerable, unused for weeks). Nothing else is selected.
Type "a"
Sleep 3.5s

# Then space picks one more item by hand: my-app's Pods.
Down@250ms 8
Sleep 1.5s
Space
Sleep 2s

# Clean: review the confirmation (it shows the total), then confirm.
Type "d"
Sleep 3.5s
Type "y"
Wait+Screen@30s /Cleaning finished/
Sleep 5s

# Back to the (now shorter) lists, then quit.
Enter
Sleep 2.5s
Type "q"
Sleep 1s
