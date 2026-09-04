---
title: 'Atom feed support'
type: 'feature'
created: '2026-09-04'
status: 'done'
review_loop_iteration: 0
baseline_commit: 'ece641387f9b71bc739a332b71c83db1762fa7a1'
context:
  - /Users/de156490/develop/podcastspeicher/_bmad-output/specs/spec-podcast-backup/SPEC.md
---

<frozen-after-approval reason="human-owned intent — do not modify unless human renegotiates">

## Intent

**Problem:** `feed.Parse` hard-rejects any document whose root element is `<feed>`, so any subscribed Atom 1.0 podcast feed silently fails to mirror. Atom feeds are structurally different from RSS 2.0 — episodes are `<entry>` elements, the unique id is `<id>`, the audio link is `<link rel="enclosure">`, and the publish date is `<updated>` — so the existing RSS XML structs cannot parse them (CAP-8).

**Approach:** Extend `feed.Parse` to detect the `<feed>` root and dispatch to a new Atom 1.0 parse branch that maps `<entry>`/`<id>`/`<link rel="enclosure">`/`<title>`/`<updated>` to the existing `Show`/`Episode` contract. The RSS 2.0 path is untouched; all callers continue to use `Parse` and `Fetch` unchanged.

## Boundaries & Constraints

**Always:**
- The existing `Show` and `Episode` types are the single contract. The Atom branch produces `Show`/`Episode` values identical in shape and semantics to the RSS branch.
- GUID for an Atom entry is the `<id>` text value, trimmed. Entries without a non-empty `<id>` get an empty GUID (same fall-through logic as RSS items with no stable id).
- Enclosure URL is taken from the first `<link rel="enclosure">` with a valid absolute http(s) href. Entries with no downloadable enclosure are skipped, exactly as RSS items are.
- Enclosure type comes from the `type` attribute on that `<link>` element.
- `PubDate` is populated from the `<updated>` element text, trimmed. No date normalisation — the raw string is stored as-is, matching the RSS branch's treatment of `<pubDate>`.
- Show title comes from the `<feed>`-level `<title>` text, trimmed.
- The RSS 2.0 parse path (root `<rss>`) is unchanged — no refactoring of existing structs or logic.
- stdlib only (`encoding/xml`); no new dependencies.

**Ask First:**
- Using any Atom field beyond `<id>`, `<link>`, `<title>`, `<updated>` (e.g. `<summary>`, `<author>`, namespaced iTunes extensions on Atom entries).
- Changing the `Show` or `Episode` struct fields or their semantics.
- Adding a new exported function instead of extending `Parse`.

**Never:**
- No full Atom namespace enforcement — match on local element names only, same pattern as the RSS structs.
- No RSS 2.0 path changes.
- No new exported types, interfaces, or packages.
- No Atom feed title/link disambiguation logic (first `<title>` child of `<feed>` wins, as with RSS channel title).

## I/O & Edge-Case Matrix

| Scenario | Input / State | Expected Output / Behavior | Error Handling |
|----------|--------------|---------------------------|----------------|
| ATOM_HAPPY | Atom 1.0 `<feed>` with 2 `<entry>` items, each with `<id>`, `<title>`, `<updated>`, `<link rel="enclosure" href="…" type="audio/mpeg">` | `Show{Title, Episodes[2]}` with correct GUID/Title/EnclosureURL/PubDate | N/A |
| ATOM_ENTRY_NO_ENCLOSURE | Entry has `<id>` and `<title>` but no `<link rel="enclosure">` | Entry skipped; not in `Episodes` | N/A |
| ATOM_ENTRY_NO_ID | Entry has no `<id>` element | `ep.GUID = ""` (empty, not an error) | N/A |
| ATOM_ENTRY_BAD_ENCLOSURE | `<link rel="enclosure" href="ep.mp3">` (relative URL) | Entry skipped | N/A |
| ATOM_FIRST_ENCLOSURE_WINS | Entry has two `<link rel="enclosure">` elements; first is valid http(s) | First one used; second ignored | N/A |
| RSS_UNCHANGED | Valid RSS 2.0 `<rss>` document | Parses exactly as before | N/A |
| UNKNOWN_ROOT | Root element is neither `<rss>` nor `<feed>` | `error: unsupported root <…>` | Caller skips show this poll |

</frozen-after-approval>

## Code Map

- `internal/feed/feed.go` -- the only file changed: `Parse` dispatch extended; new `atomFeed`/`atomEntry` XML structs added; new private `parseAtom` function
- `internal/feed/feed_test.go` -- new Atom-specific test cases; `TestParseRejectsNonRSS` updated to confirm Atom is now accepted (not rejected)
- `go.mod` -- read-only; no new dependencies

## Tasks & Acceptance

**Execution:**
- [x] `internal/feed/feed.go` -- add `atomFeed`, `atomEntry` XML structs; add `parseAtom` private function; extend `Parse` switch to dispatch `<feed>` root to `parseAtom` instead of returning an error; update package-level comment from "RSS 2.0" to "RSS 2.0 and Atom 1.0" -- adds Atom support without touching the RSS path
- [x] `internal/feed/feed_test.go` -- add `TestParseAtom` (happy path: 2 entries, correct mapping), `TestParseAtomNoEnclosure` (entry skipped), `TestParseAtomNoID` (empty GUID), `TestParseAtomBadEnclosure` (relative URL skipped), `TestParseAtomFirstEnclosureWins` (first valid link wins); update `TestParseRejectsNonRSS` to remove the `atom` case (Atom is now valid, not an error) -- covers the I/O matrix and the acceptance criteria

**Acceptance Criteria:**
- Given a valid Atom 1.0 feed document, when `Parse` is called, then it returns a `Show` with title from `<feed><title>` and `Episodes` with GUID from `<id>`, EnclosureURL from the first `<link rel="enclosure">` with a valid absolute http(s) href, EnclosureType from its `type` attribute, and PubDate from `<updated>`; entries with no downloadable enclosure are absent.
- Given the same RSS 2.0 feed document that passed before this change, when `Parse` is called, then the result is byte-for-byte identical to the pre-change output (RSS path unaffected).
- Given a feed whose root element is neither `<rss>` nor `<feed>`, when `Parse` is called, then an error is returned.

## Spec Change Log

## Design Notes

Atom `<link>` disambiguation: an Atom entry can have multiple `<link>` elements with different `rel` values (`alternate`, `enclosure`, `related`, …). Only elements with `rel="enclosure"` are candidates for the audio file. Because `encoding/xml` cannot directly filter on attributes during struct decoding, `atomEntry` collects all `<link>` elements as a slice of `atomLink` structs (capturing `rel`, `href`, and `type` attributes), and `parseAtom` iterates the slice to find the first with `rel="enclosure"` and a downloadable href.

```go
type atomLink struct {
    Rel  string `xml:"rel,attr"`
    Href string `xml:"href,attr"`
    Type string `xml:"type,attr"`
}
type atomEntry struct {
    ID      string     `xml:"id"`
    Title   string     `xml:"title"`
    Updated string     `xml:"updated"`
    Links   []atomLink `xml:"link"`
}
type atomFeed struct {
    Title   string      `xml:"title"`
    Entries []atomEntry `xml:"entry"`
}
```

## Verification

**Commands:**
- `go build ./...` -- expected: clean build
- `go vet ./...` -- expected: no findings
- `go test ./internal/feed/...` -- expected: all pass including new Atom tests

## Suggested Review Order

**Dispatch — where the new path begins**

- Root-element switch; `<feed>` now routes to `parseAtom` instead of erroring.
  [`feed.go:79`](../../../../internal/feed/feed.go#L79)

**Atom parse branch — the new logic**

- `parseAtom`: entry loop, enclosure selection, GUID/title/date mapping.
  [`feed.go:164`](../../../../internal/feed/feed.go#L164)

- Atom XML structs: `atomFeed`, `atomEntry`, `atomLink` with link-attribute capture.
  [`feed.go:196`](../../../../internal/feed/feed.go#L196)

**Tests**

- Happy-path: two entries, correct field mapping end-to-end.
  [`feed_test.go:286`](../../../../internal/feed/feed_test.go#L286)

- Edge cases: no enclosure, no `<id>`, bad enclosure URL, first-wins, empty feed, no title, no `<updated>`.
  [`feed_test.go:329`](../../../../internal/feed/feed_test.go#L329)

