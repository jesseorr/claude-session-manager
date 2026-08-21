#!/usr/bin/env python3
"""Drive cs in a pty, reconstruct each screen with pyte, and emit SVG frames.

Bubbletea repaints only the lines that changed, so the raw pty stream is not a
sequence of whole screens. pyte replays the stream into a real screen buffer,
which is the only way to capture what a viewer would actually see.
"""
import fcntl, json, os, select, struct, subprocess, sys, termios, time
from pathlib import Path

import pyte

COLS, ROWS = 118, 28
# DejaVu Sans Mono advances 0.60205 em per glyph. Deriving the font size from
# the cell width keeps a run's natural width equal to the cells it occupies;
# textLength then pins it exactly, so a fallback font cannot shift the layout.
CELL_W, CELL_H = 8.4, 19.0
FONT_SIZE = round(CELL_W / 0.60205, 2)
OUT = Path(sys.argv[1])
HOME = sys.argv[2]
CS = sys.argv[3]

# A dark palette close to a default terminal, with the 256-colour entries the
# tool actually uses filled in.
PALETTE = {
    "black": "#1c1e26", "red": "#e05252", "green": "#7fd88f", "brown": "#d7b76a",
    "blue": "#6c9ced", "magenta": "#c792ea", "cyan": "#59c2c6", "white": "#c8ccd4",
    "brightblack": "#5c6370", "brightred": "#ff6b6b", "brightgreen": "#98e08a",
    "brightbrown": "#f0d68a", "brightblue": "#8ab4f8", "brightmagenta": "#e0a6ff",
    "brightcyan": "#7fdbde", "brightwhite": "#ffffff",
    "default": "#c8ccd4",
}
BG = "#14161c"
XTERM = {"236": "#303030", "238": "#444444", "54": "#5f0087", "58": "#5f5f00",
         "124": "#af0000", "231": "#ffffff", "250": "#bcbcbc", "245": "#8a8a8a",
         "252": "#d0d0d0", "255": "#eeeeee", "221": "#ffd75f", "13": "#ff5fff",
         "10": "#5fff5f", "11": "#ffff5f", "14": "#5fffff", "9": "#ff5f5f",
         "52": "#5f0000", "1": "#e05252", "2": "#7fd88f", "3": "#d7b76a",
         "5": "#c792ea", "6": "#59c2c6"}


def colour(name, default):
    if name in PALETTE:
        return PALETTE[name]
    if name in XTERM:
        return XTERM[name]
    if len(name) == 6:
        try:
            int(name, 16)
            return "#" + name
        except ValueError:
            pass
    return default


def esc(s):
    return s.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")


def to_svg(screen):
    """One <text> run per contiguous span of identical styling."""
    w, h = COLS * CELL_W, ROWS * CELL_H
    out = [f'<svg xmlns="http://www.w3.org/2000/svg" width="{w:.0f}" height="{h:.0f}" '
           f'viewBox="0 0 {w:.0f} {h:.0f}">',
           f'<rect width="100%" height="100%" fill="{BG}" rx="6"/>',
           '<g font-family="DejaVu Sans Mono, Menlo, Consolas, monospace" '
           f'font-size="{FONT_SIZE}" xml:space="preserve">']

    for y in range(ROWS):
        line = screen.buffer[y]
        x = 0
        while x < COLS:
            ch = line[x]
            run, fg, bg, bold = [], ch.fg, ch.bg, ch.bold
            while x < COLS:
                c = line[x]
                if (c.fg, c.bg, c.bold) != (fg, bg, bold):
                    break
                run.append(c.data or " ")
                x += 1
            text = "".join(run)
            if not text.strip() and bg == "default":
                continue
            px, py = 0 + x - len(run), y
            if bg != "default":
                out.append(f'<rect x="{px * CELL_W:.2f}" y="{py * CELL_H:.2f}" '
                           f'width="{len(run) * CELL_W:.2f}" height="{CELL_H:.2f}" '
                           f'fill="{colour(bg, BG)}"/>')
            if text.strip():
                weight = ' font-weight="bold"' if bold else ""
                out.append(f'<text x="{px * CELL_W:.2f}" y="{py * CELL_H + FONT_SIZE * 0.78:.2f}" '
                           f'textLength="{len(run) * CELL_W:.2f}" lengthAdjust="spacingAndGlyphs" '
                           f'fill="{colour(fg, PALETTE["default"])}"{weight}>{esc(text)}</text>')
    out.append("</g></svg>")
    return "\n".join(out)


def main():
    steps = json.loads(Path(sys.argv[4]).read_text())
    OUT.mkdir(parents=True, exist_ok=True)

    mfd, sfd = os.openpty()
    fcntl.ioctl(sfd, termios.TIOCSWINSZ, struct.pack("HHHH", ROWS, COLS, 0, 0))
    env = dict(os.environ, HOME=HOME, TERM="xterm-256color", COLORTERM="truecolor",
               CLICOLOR_FORCE="1")
    proc = subprocess.Popen([CS], stdin=sfd, stdout=sfd, stderr=sfd,
                            env=env, close_fds=True, cwd=HOME)
    os.close(sfd)

    screen = pyte.Screen(COLS, ROWS)
    stream = pyte.Stream(screen)

    def pump(seconds):
        end = time.time() + seconds
        while time.time() < end:
            r, _, _ = select.select([mfd], [], [], 0.05)
            if r:
                try:
                    stream.feed(os.read(mfd, 65536).decode("utf8", "replace"))
                except OSError:
                    return

    frames = []
    pump(1.2)
    for i, step in enumerate(steps):
        if step["keys"]:
            os.write(mfd, step["keys"].encode().decode("unicode_escape").encode())
        pump(step.get("settle", 0.5))
        path = OUT / f"frame{i:03d}.svg"
        path.write_text(to_svg(screen))
        frames.append({"file": path.name, "hold": step.get("hold", 1.4)})
        label = step.get("label", step["keys"])
        print("  %2d  %-38s -> %s" % (i, label, path.name))

    (OUT / "frames.json").write_text(json.dumps(frames, indent=2))
    proc.kill()


main()
