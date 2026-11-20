#!/usr/bin/env python3
"""Render the joshbot demo as an animated GIF for the README.

The frames are built from the real command's output — the demo is run here,
its stdout is captured, and the frames are slices of that output. A GIF that
was typed by hand would drift from what the command actually prints; this
one cannot.

Usage: python3 scripts/render-demo-gif.py <joshbot-binary> <output.gif>
"""

import subprocess
import sys

from PIL import Image, ImageDraw, ImageFont

BG = (13, 17, 23)
FG = (201, 209, 217)
PROMPT = (126, 231, 135)
BLUE = (121, 192, 255)
PURPLE = (210, 168, 255)
ORANGE = (255, 166, 87)
DIM = (110, 118, 129)
TITLEBAR = (22, 27, 34)

FONT = "/usr/share/fonts/truetype/liberation/LiberationMono-Regular.ttf"
FONT_BOLD = "/usr/share/fonts/truetype/liberation/LiberationMono-Bold.ttf"

SIZE = 17
PAD = 26
LINE_H = 26
WIDTH = 880


def run_demo(binary):
    # A fixed workspace, so the paths in the transcript are the same every
    # time the GIF is rebuilt — a random temporary directory in a README
    # image looks like a mistake.
    workspace = "/tmp/joshbot-demo-workspace"
    subprocess.run(["rm", "-rf", workspace], check=False)
    out = subprocess.run(
        [binary, "demo", "--workspace", workspace],
        capture_output=True,
        text=True,
        timeout=120,
    )
    return out.stdout


def split_frames(text):
    """Slice the demo's output into the frames the GIF shows."""
    lines = [ln.rstrip() for ln in text.splitlines()]
    # Drop the leading blank line the demo prints.
    while lines and not lines[0].strip():
        lines.pop(0)

    frames = []
    current = ["$ joshbot demo"]

    for line in lines:
        if line.strip() == "":
            continue
        stripped = line.strip()
        # A new frame starts at each model step and at the answer, so the
        # GIF unfolds the turn instead of showing it all at once.
        starts_step = stripped.startswith("model") and len(current) > 1
        starts_answer = stripped.startswith("joshbot") and len(current) > 1
        if starts_step or starts_answer:
            frames.append(current)
            current = [line]
            continue
        current.append(line)
    frames.append(current)

    # Merge the opening header into the first frame and keep the last two
    # frames whole: the closing paragraph is the point of the GIF.
    if len(frames) < 3:
        return frames
    return frames


def colour_for(line):
    stripped = line.strip()
    if stripped.startswith("$"):
        return PROMPT
    if stripped.startswith("you"):
        return BLUE
    if stripped.startswith("model"):
        return PURPLE
    if stripped.startswith("tool"):
        return ORANGE
    if stripped.startswith("joshbot"):
        return PROMPT
    return FG


def render_frame(lines, title):
    height = PAD * 2 + LINE_H * max(len(lines), 1) + 34
    img = Image.new("RGB", (WIDTH, height), BG)
    d = ImageDraw.Draw(img)

    # A title bar, so the GIF reads as a terminal rather than a code sample.
    d.rectangle([0, 0, WIDTH, 30], fill=TITLEBAR)
    for i, colour in enumerate([(255, 95, 86), (255, 189, 46), (39, 201, 63)]):
        d.ellipse([14 + i * 18, 10, 24 + i * 18, 20], fill=colour)
    font_title = ImageFont.truetype(FONT, 13)
    d.text((78, 8), title, font=font_title, fill=DIM)

    font = ImageFont.truetype(FONT, SIZE)
    y = 30 + PAD
    for line in lines:
        d.text((PAD, y), line, font=font, fill=colour_for(line))
        y += LINE_H
    return img


def main():
    binary = sys.argv[1]
    out_path = sys.argv[2]

    text = run_demo(binary)
    frames = split_frames(text)
    print(f"{len(frames)} frames")

    images = [render_frame(f, "joshbot — 19MB, no Python, no venv") for f in frames]
    # A GIF needs every frame the same size.
    size = (WIDTH, max(i.height for i in images))
    padded = []
    for i in images:
        if i.height != size[1]:
            canvas = Image.new("RGB", size, BG)
            canvas.paste(i, (0, 0))
            i = canvas
        padded.append(i.convert("P", palette=Image.ADAPTIVE, colors=64))

    padded[0].save(
        out_path,
        save_all=True,
        append_images=padded[1:],
        duration=1800,
        loop=0,
        optimize=True,
        disposal=2,
    )
    print(f"wrote {out_path}")


if __name__ == "__main__":
    main()
