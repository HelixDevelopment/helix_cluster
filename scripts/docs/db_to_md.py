#!/usr/bin/env python3
# -----------------------------------------------------------------------------
# Purpose:      Regenerate the workable-item markdown views (issues.md,
#               issues_summary.md, fixed.md, fixed_summary.md) FROM the canonical
#               SQLite registry (data/hxc_registry.db) — making the DB the single
#               source of truth per Constitution §11.4.93. Compact per-phase
#               tables so every active item carries a Status, while keeping the
#               docs_chain html/pdf exports a sane size.
# Usage:        python3 scripts/docs/db_to_md.py
# Inputs:       HXC_DB env (default data/hxc_registry.db).
# Outputs:      docs/{issues,issues_summary,fixed,fixed_summary}.md (revision-bumped).
# Side-effects: Overwrites those four markdown files (docs_chain then re-exports).
# Dependencies: python3 stdlib (sqlite3).
# Cross-refs:   Constitution §11.4.93/.95/.15/.16/.44; .docs_chain (future db-to-md
#               transform); scripts/docs/run_docs_chain.sh.
# -----------------------------------------------------------------------------
import os, re, sqlite3, datetime

DB = os.environ.get("HXC_DB", "data/hxc_registry.db")
PHASE_NAME = {0: "Foundation (anti-bluff)", 1: "MVP", 2: "Phase 2", 3: "Phase 3",
              4: "Phase 4", 5: "Phase 5", 6: "Phase 6", 7: "Phase 7", 8: "Phase 8",
              9: "Phase 8B", 10: "Phase 8C"}
NOW = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def bump_rev(path):
    try:
        m = re.search(r'^\*\*Revision:\*\* (\d+)', open(path).read(), re.M)
        return int(m.group(1)) + 1 if m else 1
    except FileNotFoundError:
        return 1


def header(title, desc, rev):
    return (f"# {title}\n\n**Revision:** {rev}\n**Last modified:** {NOW}\n"
            f"**Description:** {desc}\n**Authority:** Constitution §11.4.93 (workable-items DB single source of truth)\n"
            f"**Generated-by:** scripts/docs/db_to_md.py (DB is canonical; edit via cmd/hxc-registry, not by hand)\n\n")


def esc(s):
    return (s or "").replace("|", "\\|").replace("\n", " ").strip()


def main():
    con = sqlite3.connect(DB)
    con.row_factory = sqlite3.Row
    rows = con.execute("SELECT * FROM items").fetchall()
    active = [r for r in rows if r["current_location"] == "Issues"]
    fixed = [r for r in rows if r["current_location"] == "Fixed"]

    # ---- issues.md ----
    rev = bump_rev("docs/issues.md")
    out = [header("Issues", "Active workable-item registry for Helix Cluster OS", rev)]
    out.append(f"Total active items: **{len(active)}**. Canonical source: `data/hxc_registry.db`. "
               "Full per-item detail (description, closure criteria, required test types, source refs) "
               "lives in the DB and `docs/research/_ledger/*.json`.\n\n")
    for ph in sorted(set(r["phase"] for r in active)):
        items = sorted([r for r in active if r["phase"] == ph],
                       key=lambda r: (r["priority"], r["hxc_id"]))
        out.append(f"## {PHASE_NAME.get(ph, 'Phase '+str(ph))} ({len(items)} active)\n\n")
        out.append("| HXC | Type | Pri | Status | Title |\n|---|---|---|---|---|\n")
        for r in items:
            out.append(f"| {r['hxc_id']} | {r['type']} | {r['priority']} | {r['status']} | {esc(r['title'])} |\n")
        out.append("\n")
    open("docs/issues.md", "w").write("".join(out))

    # ---- issues_summary.md ----
    rev = bump_rev("docs/issues_summary.md")
    s = [header("Issues Summary", "Counts of active workable items by phase / type / status / priority", rev)]
    def tally(key):
        d = {}
        for r in active:
            d[r[key]] = d.get(r[key], 0) + 1
        return d
    s.append(f"**Total active:** {len(active)}\n\n### By phase\n\n| Phase | Count |\n|---|---|\n")
    for ph in sorted(set(r["phase"] for r in active)):
        s.append(f"| {PHASE_NAME.get(ph,ph)} | {sum(1 for r in active if r['phase']==ph)} |\n")
    for label, key in [("type", "type"), ("priority", "priority"), ("status", "status")]:
        s.append(f"\n### By {label}\n\n| {label.title()} | Count |\n|---|---|\n")
        for k, v in sorted(tally(key).items()):
            s.append(f"| {k} | {v} |\n")
    open("docs/issues_summary.md", "w").write("".join(s))

    # ---- fixed.md ----
    rev = bump_rev("docs/fixed.md")
    f = [header("Fixed", "Completed workable items (with evidence references)", rev)]
    f.append(f"Total completed: **{len(fixed)}**.\n\n")
    f.append("| HXC | Type | Pri | Title | Commit |\n|---|---|---|---|---|\n")
    for r in sorted(fixed, key=lambda r: r["hxc_id"]):
        f.append(f"| {r['hxc_id']} | {r['type']} | {r['priority']} | {esc(r['title'])} | {esc(r['commit_sha'])} |\n")
    open("docs/fixed.md", "w").write("".join(f))

    # ---- fixed_summary.md ----
    rev = bump_rev("docs/fixed_summary.md")
    fs = [header("Fixed Summary", "Counts of completed workable items", rev)]
    fs.append(f"**Total completed:** {len(fixed)}\n\n### By type\n\n| Type | Count |\n|---|---|\n")
    td = {}
    for r in fixed:
        td[r["type"]] = td.get(r["type"], 0) + 1
    for k, v in sorted(td.items()):
        fs.append(f"| {k} | {v} |\n")
    open("docs/fixed_summary.md", "w").write("".join(fs))

    con.close()
    print(f"regenerated: issues.md ({len(active)} active), fixed.md ({len(fixed)} completed) + summaries")


if __name__ == "__main__":
    main()
