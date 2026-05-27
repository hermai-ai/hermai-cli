# State Enforcement Actions

## Product framing

Hermai should be a canonical, cited, refreshable index of state professional-license enforcement actions. The value is not generic PDF scraping; it is record-level provenance that can be audited against source documents.

## Phases

1. Illinois proof of concept
   - Discover IDFPR monthly disciplinary PDFs from the root report page.
   - Extract every enforcement action into cited JSON.
   - Preserve source URL, report month, page number, source text, and source text hash.
   - Use deterministic verification signals instead of LLM confidence scores.

2. PDF and OCR foundation
   - Support embedded-text PDFs first.
   - Treat OCR fallback as a blocker for the 10-state milestone, not a late 50-state concern.
   - Flag scanned or empty-text PDFs with `ocr_required`.

3. Ten-state expansion
   - Add state configs and parser profiles.
   - Reuse the same output model: raw source fields plus normalized fields.
   - Stagger refreshes and cache source documents to avoid hammering agency websites.

4. Search product
   - Index people, actions, professions, descriptions, source text, and regulation references.
   - Keep statute mapping as phase 2 product work because many states describe violations without explicit citations.

## Data model requirements

- Preserve raw fields such as `profession_group_raw` and `license_type_raw`.
- Add normalized fields such as `profession_normalized` during extraction so search does not need to know every synonym.
- Include `entity_resolution_hint` now so later person/license disambiguation can be added without reshaping records. This is a provisional helper, not a stable identity contract.
- Use append-only extracted records with `source_text_sha256` and future supersedes pointers. File checksums alone are insufficient because agencies can re-issue corrected reports.
- Include deterministic checks such as case-number pattern matches, page presence, clean name parsing, source text hash, and cross-extractor agreement when available.

## Quality bar

- Start with a small Illinois fixture for regression tests.
- Grow to a 300+ row gold set across months and boards before shipping search.
- Sample long-tail cases: multi-license rows, page-boundary rows, out-of-state locations, business names, application IDs, missing case numbers, and statute references.

## Policy and operations

- Define a takedown/expungement policy before launch.
- Track source removal separately from record deletion.
- Refresh politely with staggered schedules, caching, and per-state rate limits.
- Surface extraction warnings rather than hiding low-quality rows.
