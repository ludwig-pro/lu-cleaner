# scan.tape — non-interactive: `scan --summary`, then the plan of the
# recommended cleanup with `clean --yes --smart --dry-run` (nothing deleted).
# Synthetic sandbox only: record with demo/record.sh scan.
# poster: 18s (frame used as the video poster by demo/record.sh)

Output site/public/demos/scan.gif
Output site/public/demos/scan.mp4

Source demo/tapes/settings.tape

Sleep 600ms
Type "lu-cleaner scan --summary"
Sleep 400ms
Enter
Wait+Line@60s /❯ *$/
Sleep 4.5s

Ctrl+L
Sleep 400ms
Type "lu-cleaner clean --yes --smart --dry-run"
Sleep 500ms
Enter
Wait+Line@60s /❯ *$/
Sleep 6s
