# worktrees.tape — `lu-cleaner worktrees`: the worktrees left by AI agents with
# their status; remove a merged one; a dirty one is refused by git's checks.
# Synthetic sandbox only: record with demo/record.sh worktrees.
# poster: 20s (frame used as the video poster by demo/record.sh)
# busy: codex|Codex (only these guards matter here: no Xcode-guarded item)

Output site/public/demos/worktrees.gif
Output site/public/demos/worktrees.mp4

Source demo/tapes/settings.tape

# The table: tool, branch and status of every worktree.
Sleep 600ms
Type "lu-cleaner worktrees --list"
Sleep 400ms
Enter
Wait+Line@60s /❯ *$/
Sleep 4s

# The picker: nothing is preselected here.
Type "lu-cleaner worktrees"
Sleep 400ms
Enter
Wait+Screen@30s /scan done/
Sleep 2.5s

# Select the Codex worktree (merged, pushed, idle for 3 weeks)...
Space
Sleep 1.2s

# ...and, to see what happens, the Cursor one with uncommitted work.
Down 2
Sleep 2.5s
Space
Sleep 1.5s

# Caution items need a typed "yes".
Type "d"
Sleep 3s
Type "yes"
Sleep 800ms
Enter
Wait+Screen@30s /Cleaning finished/
Sleep 5.5s

# The dirty worktree is still there, its work untouched.
Enter
Sleep 3s
Type "q"
Sleep 1s
