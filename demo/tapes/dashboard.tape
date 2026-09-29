# dashboard.tape — `lu-cleaner`: scan, browse worktrees and project artifacts,
# add an item to the smart selection, clean, summary. Synthetic sandbox only:
# record with demo/record.sh dashboard.
# poster: 7s (frame used as the video poster by demo/record.sh)

Output site/public/demos/dashboard.gif
Output site/public/demos/dashboard.mp4

Source demo/tapes/settings.tape

Sleep 600ms
Type "lu-cleaner"
Sleep 400ms
Enter

# Scanners stream their results; smart select preselects the recommended items.
Wait+Screen@30s /scan done/
Sleep 3s

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

# Add my-app's Pods to the selection.
Down@250ms 8
Sleep 1.5s
Space
Sleep 1.5s

# Clean: review the confirmation, then confirm.
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
