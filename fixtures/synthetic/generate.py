#!/usr/bin/env python3
"""Regenerate the synthetic Groups.io contact fixtures.

The two output files mirror the shape of a real Groups.io "table" export
without containing any real BCARS data. See README.md for case coverage.

Run: python3 fixtures/synthetic/generate.py
"""
from __future__ import annotations

import csv
import io
import json
import pathlib
import sys

HERE = pathlib.Path(__file__).resolve().parent

# Column definitions mirror the observed Groups.io export exactly.
COLUMNS = [
    (1,  "Contact Name",       "text_column"),
    (11, "Call Sign",          "text_column"),
    (3,  "Current Until",      "date_column"),
    (14, "Note",               "text_column"),
    (2,  "Membership Type",    "text_column"),
    (13, "Class",              "text_column"),
    (7,  "Phone",              "text_column"),
    (4,  "Email",              "text_column"),
    (5,  "Street Address",     "text_column"),
    (6,  "City",               "text_column"),
    (9,  "Postal Code",        "text_column"),
    (8,  "State/Province",     "text_column"),
    (12, "Volunteer Examiner", "checkbox_column"),
]
COL_ID = {name: cid for cid, name, _ in COLUMNS}
COL_TYPE = {name: kind for _, name, kind in COLUMNS}

# Each row is a dict of column-name -> value. Values use the raw string forms
# a real Groups.io export produces (dates as MM/DD/YYYY, checkboxes as bool).
ROWS: list[dict] = []

def row(**vals) -> dict:
    ROWS.append(vals)

# --- 8 clean Full members -------------------------------------------------
for i in range(1, 9):
    row(
        **{
            "Contact Name": f"Fulltest{i} Member",
            "Call Sign":    f"KA9F{i:02d}X",
            "Current Until":"12/31/2026",
            "Note":         "",
            "Membership Type":"Full",
            "Class":        "General",
            "Phone":        f"555-010-{1000+i:04d}",
            "Email":        f"full{i}@example.invalid",
            "Street Address":f"{100+i} Fake St",
            "City":         "Butler",
            "Postal Code":  "16001",
            "State/Province":"PA",
            "Volunteer Examiner": (i % 4 == 0),
        }
    )

# --- 4 clean Associate members -------------------------------------------
for i in range(1, 5):
    row(
        **{
            "Contact Name": f"Assoctest{i} Person",
            "Call Sign":    "",
            "Current Until":"12/31/2026",
            "Note":         "",
            "Membership Type":"Associate",
            "Class":        "",
            "Phone":        f"555-020-{2000+i:04d}",
            "Email":        f"assoc{i}@example.invalid",
            "Street Address":f"{200+i} Fake Ave",
            "City":         "Butler",
            "Postal Code":  "16001",
            "State/Province":"PA",
            "Volunteer Examiner": False,
        }
    )

# --- 2 known lifetime honorary rows (must default to lifetime Associate) --
row(
    **{
        "Contact Name": "Lifetimetest One",
        "Call Sign":    "KA9L01X",
        "Current Until":"12/31/2055",
        "Note":         "Passed initial exam via BCARS session (synthetic)",
        "Membership Type":"Honorary",
        "Class":        "Technician",
        "Phone":        "555-030-0001",
        "Email":        "life1@example.invalid",
        "Street Address":"301 Fake Ln",
        "City":         "Butler",
        "Postal Code":  "16001",
        "State/Province":"PA",
        "Volunteer Examiner": False,
    }
)
row(
    **{
        "Contact Name": "Lifetimetest Two",
        "Call Sign":    "KA9L02X",
        "Current Until":"12/31/2055",
        "Note":         "Longtime service; synthetic lifetime honorary",
        "Membership Type":"Honorary",
        "Class":        "General",
        "Phone":        "555-030-0002",
        "Email":        "life2@example.invalid",
        "Street Address":"302 Fake Ln",
        "City":         "Butler",
        "Postal Code":  "16001",
        "State/Province":"PA",
        "Volunteer Examiner": False,
    }
)
# External ids for these two rows are 900001 and 900002 respectively.

# --- Unexpected lifetime-like date (needs manual confirmation) ------------
row(
    **{
        "Contact Name": "Suspicious Date",
        "Call Sign":    "KA9S01X",
        "Current Until":"12/31/2055",
        "Note":         "",
        "Membership Type":"Full",
        "Class":        "General",
        "Phone":        "555-040-0001",
        "Email":        "sus1@example.invalid",
        "Street Address":"401 Fake Rd",
        "City":         "Butler",
        "Postal Code":  "16001",
        "State/Province":"PA",
        "Volunteer Examiner": False,
    }
)

# --- Unknown paid-through -------------------------------------------------
row(
    **{
        "Contact Name": "Unknowndate Sample",
        "Call Sign":    "KA9U01X",
        "Current Until":"01/01/0001",
        "Note":         "",
        "Membership Type":"Full",
        "Class":        "Technician",
        "Phone":        "555-050-0001",
        "Email":        "unk1@example.invalid",
        "Street Address":"501 Fake Way",
        "City":         "Butler",
        "Postal Code":  "16001",
        "State/Province":"PA",
        "Volunteer Examiner": False,
    }
)

# --- Ambiguous email (two persons share it) -------------------------------
for i, first in enumerate(["Ambigone", "Ambigtwo"], start=1):
    row(
        **{
            "Contact Name": f"{first} Shared",
            "Call Sign":    f"KA9A0{i}X",
            "Current Until":"12/31/2026",
            "Note":         "",
            "Membership Type":"Full",
            "Class":        "General",
            "Phone":        f"555-060-000{i}",
            "Email":        "shared@example.invalid",
            "Street Address":f"{600+i} Shared Ct",
            "City":         "Butler",
            "Postal Code":  "16001",
            "State/Province":"PA",
            "Volunteer Examiner": False,
        }
    )

# --- Honorary with unspecified type ---------------------------------------
row(
    **{
        "Contact Name": "Honorary Unspecified",
        "Call Sign":    "",
        "Current Until":"12/31/2026",
        "Note":         "Dues waived; base type unspecified in export",
        "Membership Type":"Honorary",
        "Class":        "",
        "Phone":        "555-070-0001",
        "Email":        "honnun@example.invalid",
        "Street Address":"701 Fake Blvd",
        "City":         "Butler",
        "Postal Code":  "16001",
        "State/Province":"PA",
        "Volunteer Examiner": False,
    }
)

# --- Invalid phone --------------------------------------------------------
row(
    **{
        "Contact Name": "Badphone Sample",
        "Call Sign":    "KA9B01X",
        "Current Until":"12/31/2026",
        "Note":         "",
        "Membership Type":"Full",
        "Class":        "Technician",
        "Phone":        "call me maybe",
        "Email":        "bad1@example.invalid",
        "Street Address":"801 Fake Cir",
        "City":         "Butler",
        "Postal Code":  "16001",
        "State/Province":"PA",
        "Volunteer Examiner": False,
    }
)

# --- Case-fold membership type -------------------------------------------
row(
    **{
        "Contact Name": "Lowercase Full",
        "Call Sign":    "KA9C01X",
        "Current Until":"12/31/2026",
        "Note":         "",
        "Membership Type":"full",
        "Class":        "General",
        "Phone":        "555-090-0001",
        "Email":        "case1@example.invalid",
        "Street Address":"901 Fake Pl",
        "City":         "Butler",
        "Postal Code":  "16001",
        "State/Province":"PA",
        "Volunteer Examiner": False,
    }
)

# --- Serialize ------------------------------------------------------------

def build_vals(rowdict: dict) -> list[dict]:
    """Convert a plain dict to Groups.io's per-cell `vals` shape."""
    out = []
    for _, name, kind in COLUMNS:
        v = rowdict.get(name, "")
        cell = {"col_id": COL_ID[name], "col_type": kind}
        if kind == "checkbox_column":
            cell["checked"] = bool(v)
        elif kind == "date_column":
            # Groups.io emits ISO-ish strings. Real exports use MM/DD/YYYY in
            # a `text` field; keep parity with what we observed.
            cell["text"] = v or ""
        else:
            cell["text"] = "" if v is None else str(v)
        out.append(cell)
    return out


def emit_json() -> str:
    # Deterministic external ids so tests can pin the "known lifetime" rows.
    external_ids = list(range(900000, 900000 + len(ROWS)))
    # Force life1/life2 to 900001/900002 (they are at positions 12/13 as
    # generated above; assert to catch drift).
    life_positions = [i for i, r in enumerate(ROWS)
                      if r["Contact Name"].startswith("Lifetimetest")]
    assert len(life_positions) == 2, "expected exactly two lifetime rows"
    external_ids[life_positions[0]] = 900001
    external_ids[life_positions[1]] = 900002

    doc = {
        "table": {
            "id": 999001,
            "object": "databasetable",
            "created": "2026-01-01T00:00:00Z",
            "updated": "2026-01-01T00:00:00Z",
            "group_id": 999000,
            "user_id": 1,
            "name": "Synthetic BCARS Contact Database",
            "short_desc": "Synthetic test fixture",
            "desc": "<div>Synthetic; contains no real member data.</div>",
            "desc_type": "html",
            "edit_table": "database_access_restricted",
            "edit_rows": "database_access_restricted",
            "add_rows": "database_access_restricted",
            "view_table": "database_access_restricted",
            "num_rows": len(ROWS),
            "max_row_id": max(external_ids),
            "num_columns": len(COLUMNS),
            "max_col_id": max(cid for cid, _, _ in COLUMNS),
            "display_template": "",
            "columns": [
                {
                    "id": cid, "name": name, "type": kind,
                    "required": False, "color": "color_none",
                    "width": 0, "default_hidden": False, "description": "",
                }
                for cid, name, kind in COLUMNS
            ],
        },
        "rows": [
            {
                "id": external_ids[i],
                "object": "databaserow",
                "created": "2026-01-01T00:00:00Z",
                "updated": "2026-01-01T00:00:00Z",
                "group_id": 999000,
                "table_id": 999001,
                "row_num": i + 1,
                "vals": build_vals(r),
                "num_vals": len(COLUMNS),
            }
            for i, r in enumerate(ROWS)
        ],
    }
    return json.dumps(doc, indent=2, ensure_ascii=False) + "\n"


def emit_csv() -> str:
    buf = io.StringIO()
    header = [name for _, name, _ in COLUMNS]
    w = csv.writer(buf, lineterminator="\n")
    w.writerow(header)
    for r in ROWS:
        line = []
        for name in header:
            v = r.get(name, "")
            if COL_TYPE[name] == "checkbox_column":
                line.append("true" if v else "false")
            else:
                line.append("" if v is None else str(v))
        w.writerow(line)
    return buf.getvalue()


def main() -> int:
    (HERE / "groupsio_contact.json").write_text(emit_json())
    (HERE / "groupsio_contact.csv").write_text(emit_csv())
    print(f"wrote {len(ROWS)} rows to groupsio_contact.{{json,csv}}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
