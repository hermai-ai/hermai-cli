# Illinois Enforcement Extraction Audit - 2026-05-26

## Scope

- Root URL: `https://idfpr.illinois.gov/news/disciplines/discreports.html`
- Parser profile: `monthly_pdf_grouped_by_profession`
- Source filter: IDFPR enforcement report PDFs matching `/discpln/YYYY-MMenf.pdf`
- Run mode: single-threaded full backfill with a 250 ms delay between PDFs

## Backfill Results

- Discovered source PDFs: 146
- Selected source PDFs: 146
- Processed source PDFs: 146
- Extracted actions: 33,004
- Reports with zero extracted actions: 0
- Corpus-level extraction warnings: 0
- Reports with warnings: 0
- Reports requiring OCR: 0

## Quality Flags

Quality flags are deterministic review signals, not model confidence scores.

- Missing case numbers: 1,160
- Missing case numbers not marked as unlicensed actions: 0
- Empty raw profession groups: 16
- Short descriptions under 20 characters: 9
- Median per-report quality-flag rate: 2.94%
- Worst per-report quality-flag rate: 16.93% (`2016-09`)

Most missing case numbers are expected unlicensed-practice actions where IDFPR does not publish a license or case number in the action row.

## Per-Report Outliers

Highest quality-flag rates:

| Report | Actions | Quality Flags | Rate |
| --- | ---: | ---: | ---: |
| 2016-09 | 189 | 32 | 16.93% |
| 2014-01 | 125 | 20 | 16.00% |
| 2015-08 | 184 | 29 | 15.76% |
| 2015-09 | 191 | 29 | 15.18% |
| 2014-12 | 138 | 20 | 14.49% |
| 2015-04 | 197 | 26 | 13.20% |
| 2015-11 | 158 | 20 | 12.66% |
| 2014-10 | 208 | 26 | 12.50% |

## Manual Spot-Check

Ten records sampled across 2014-2026 were checked against their extracted source text. All 10 matched the source text for defendant name, location, case number where published, profession group, page, and description.

Spot-checked reports:

- 2014-01: Prospect Mortgage, LLC
- 2015-06: Philip Achusim
- 2016-06: Dustin Jacoby
- 2017-03: Lion Financial Business Group, LLC
- 2018-06: Jodi Pelegrin
- 2019-07: Annelee Mendoza
- 2021-10: Treo V Le
- 2022-10: Dominic A. White
- 2025-12: Feras Allahham
- 2026-03: Steven Amu

## Known Limitations

- `license_type_raw` is heuristic and should not yet be treated as a controlled search facet.
- Explicit statute references are extracted when present, but semantic statute mapping from action language is not implemented.
- Entity resolution is a structured hint only, not a stable person identity.
- Empty raw profession groups and short descriptions are surfaced as audit flags for manual review.
- The current PR does not include persistent append-only storage; it emits source text hashes and an intended refresh strategy for the future storage layer.
