# Shared by every demo tape (Source demo/tapes/settings.tape).
# Run the tapes through demo/record.sh: it builds the binary, creates the
# synthetic sandbox and exports LU_DEMO_ENV (the sandbox's env.sh).

Set Shell "bash"
Set FontSize 15
Set Width 1200
Set Height 720
Set Padding 18
Set LineHeight 1.1
Set Theme "Catppuccin Mocha"
Set WindowBar Colorful
Set BorderRadius 10
Set TypingSpeed 70ms
Set Framerate 30
Set CursorBlink false

# Commands that print a lot scroll the screen, and Wait+Screen only sees the
# first page: wait for the prompt to come back instead (Wait+Line /❯ *$/).

# Hidden setup (demo/tapes/shell.sh): the `lu-cleaner` function of the sandbox
# (HOME, PATH and a macOS sandbox profile pointing at fake data), a short
# prompt, a clean screen.
Hide
Type "source demo/tapes/shell.sh"
Enter
Sleep 800ms
Show
