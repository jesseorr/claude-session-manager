# Regenerating the demo

`docs/demo.gif` is real output from `cs`, recorded against an invented session
store so that no actual work appears in it.

Three steps produce it. `fixture.py` writes a synthetic `~/.claude` containing
five made-up sessions. `record.py` runs `cs` against that home inside a pty and
saves one SVG per keystroke. ImageMagick then rasterises the frames and joins
them.

Claude Code repaints only the lines that changed, so the raw terminal output is
not a series of whole screens. `record.py` replays the stream through `pyte`, a
terminal emulator, and reads the screen buffer after each step. That buffer is
what a viewer sees.

```bash
cd docs/demo
go build -o /tmp/cs ../..
python3 fixture.py /tmp/demo-home
uv run --with pyte python record.py /tmp/frames /tmp/demo-home /tmp/cs steps.json

cd /tmp/frames
for f in frame*.svg; do magick -background none "$f" "${f%.svg}.png"; done
magick -loop 0 -delay 140 frame*.png -layers optimize demo.gif
```

Use `steps.json` to change what the demo shows. Each entry sends one keystroke,
waits `settle` seconds for the screen to catch up, and holds the resulting frame
for `hold` seconds.

`fixture.py` starts two `sleep` processes. They stand in for sessions that are
still running, which is what puts the `●live` marker on two rows. They exit on
their own after 15 minutes.
