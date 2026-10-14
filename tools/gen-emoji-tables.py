#!/usr/bin/env python3
"""Regenerate the emoji tables the bridge uses to decode Teams reaction keys.

Teams names a reaction by a key, not by the emoji.  Two kinds of key occur:
names from Teams' own emoticon set ("laugh", "speechless", "hearteyes") and
Unicode emoji names run together ("smilingfacewithtear", "pinchedfingers").
This writes one table for each, both read by pkg/connector/reactions.go
(embedded) and by the matrix repo's scripts/decode-reactions.py:

  pkg/connector/emoji/teams.json    key -> emoji, lifted from the Teams web
                                    app's JavaScript, in Teams' order
  pkg/connector/emoji/unicode.json  run-together Unicode emoji name -> emoji

    tools/gen-emoji-tables.py                  # find the Teams script itself
    tools/gen-emoji-tables.py --teams-js URL   # or name it (URL or file)

The Teams script is a hashed bundle on Microsoft's public CDN whose name
changes with each Teams release.  Without --teams-js, the bundles the
teams-refresh browser profile has cached are tried, newest first, until one
holds the table.  Run from mautrix-teams-src/.
"""
import argparse
import datetime
import json
import os
import re
import sys
import urllib.request
from pathlib import Path

OUT = Path(__file__).resolve().parent.parent / "pkg" / "connector" / "emoji"
PROFILE = Path(os.environ.get("TEAMS_REFRESH_DIR", "~/.local/share/teams-refresh")).expanduser() / "chrome-profile"
EMOJI_TEST = "https://unicode.org/Public/emoji/latest/emoji-test.txt"
UNICODE_DATA = "https://unicode.org/Public/UCD/latest/ucd/UnicodeData.txt"
# A pair known to be in the table, to find the Map literal that holds it.
ANCHOR = '["expressionless","\\u{1F611}"]'
SKIN_TONES = {chr(c) for c in range(0x1F3FB, 0x1F400)}


def fetch(source):
    if re.match(r"https?://", source):
        req = urllib.request.Request(source, headers={"User-Agent": "gen-emoji-tables"})
        with urllib.request.urlopen(req, timeout=60) as resp:
            return resp.read().decode("utf-8")
    return Path(source).read_text(encoding="utf-8")


def cached_bundle_urls():
    """Teams CDN script URLs named in the browser profile's code cache, newest first."""
    cache = PROFILE / "Default" / "Code Cache" / "js"
    if not cache.is_dir():
        return []
    urls = {}
    for f in cache.iterdir():
        if not f.is_file():
            continue
        data = f.read_bytes()
        if b"expressionless" not in data:
            continue
        for u in re.findall(rb"https://teams\.public\.onecdn\.static\.microsoft/[\x21-\x7e]+?\.js", data):
            urls[u.decode()] = max(urls.get(u.decode(), 0), f.stat().st_mtime)
    return sorted(urls, key=lambda u: urls[u], reverse=True)


def unescape(js):
    return re.sub(r"\\u\{([0-9A-Fa-f]+)\}|\\u([0-9A-Fa-f]{4})",
                  lambda m: chr(int(m.group(1) or m.group(2), 16)), js)


def teams_pairs(js):
    """The [key, emoji] pairs of the Map literal that contains ANCHOR, or None."""
    at = js.find(ANCHOR)
    if at < 0:
        return None
    start = js.rfind("new Map([", 0, at)
    end = js.find("])", at)
    pairs = re.findall(r'\["([a-z0-9_-]+)","((?:\\u\{[0-9A-Fa-f]+\}|\\u[0-9A-Fa-f]{4})+)"\]',
                       js[start:end])
    return [[key, unescape(emoji)] for key, emoji in pairs]


def run_together(name):
    return re.sub(r"[^a-z0-9]", "", name.lower())


def unicode_names():
    """Run-together names of every fully-qualified emoji without a skin tone.

    Each emoji gets its CLDR name ("smiling face with tear") and, for single
    code points, its Unicode character name ("HUNDRED POINTS SYMBOL", which
    CLDR now calls "hundred points"); each also without a trailing "face",
    which Teams drops ("expressionless").  The first emoji to claim a name
    keeps it.
    """
    char_names = {}
    for line in fetch(UNICODE_DATA).splitlines():
        code, name = line.split(";")[:2]
        if not name.startswith("<"):
            char_names[chr(int(code, 16))] = name
    names, version = {}, None
    for line in fetch(EMOJI_TEST).splitlines():
        if line.startswith("# Version:"):
            version = line.split(":", 1)[1].strip()
        m = re.match(r"^[0-9A-F ]+;\s*fully-qualified\s*#\s*(\S+)\s+E[\d.]+\s+(.+)$", line)
        if not m or SKIN_TONES & set(m.group(1)):
            continue
        emoji = m.group(1)
        candidates = [m.group(2)]
        bare = emoji.replace("\ufe0f", "")
        if len(bare) == 1 and bare in char_names:
            candidates.append(char_names[bare])
        for name in candidates:
            key = run_together(name)
            for k in (key, key[:-4] if key.endswith("face") and len(key) > 4 else None):
                if k:
                    names.setdefault(k, emoji)
    return version, names


def write(name, header, body_key, body):
    """HEADER's fields, then BODY under BODY_KEY one entry per line, for readable diffs."""
    path = OUT / name
    path.parent.mkdir(parents=True, exist_ok=True)
    dump = lambda v: json.dumps(v, ensure_ascii=False)
    lines = [f" {dump(k)}: {dump(v)}," for k, v in header.items()]
    if isinstance(body, dict):
        entries = [f"  {dump(k)}: {dump(v)}" for k, v in body.items()]
        open_, close = "{", "}"
    else:
        entries = [f"  {dump(v)}" for v in body]
        open_, close = "[", "]"
    text = "{\n" + "\n".join(lines) + f"\n {dump(body_key)}: {open_}\n" + ",\n".join(entries) + f"\n {close}\n}}\n"
    json.loads(text)
    path.write_text(text, encoding="utf-8")
    print(f"wrote {path}")


def main():
    ap = argparse.ArgumentParser(description=(__doc__ or "").split("\n\n")[0])
    ap.add_argument("--teams-js", help="URL or file of the Teams script holding the table")
    args = ap.parse_args()

    sources = [args.teams_js] if args.teams_js else cached_bundle_urls()
    if not sources:
        sys.exit("No Teams script found: pass --teams-js, or run teams-refresh once so its profile caches one.")
    for source in sources:
        pairs = teams_pairs(fetch(source))
        if pairs:
            break
        print(f"no table in {source}", file=sys.stderr)
    else:
        sys.exit("None of the Teams scripts holds the emoticon table.")

    today = datetime.date.today().isoformat()
    write("teams.json", {"source": source, "generated": today}, "pairs", pairs)
    version, names = unicode_names()
    write("unicode.json", {"source": EMOJI_TEST, "version": version, "generated": today},
          "names", dict(sorted(names.items())))
    print(f"{len(pairs)} Teams keys, {len(names)} Unicode names")


if __name__ == "__main__":
    main()
